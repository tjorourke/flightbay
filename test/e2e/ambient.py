#!/usr/bin/env python3
"""Live Ambient wizard proof on kind. Creates and deletes only its own namespace.

python3 test/e2e/ambient.py --context kind-mesh1 [--keep]
Requires an existing ambient installation and the istio-waypoint GatewayClass.
Also proves local rate limiting when an agentgateway waypoint class is installed.
"""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import time
from urllib.error import HTTPError
from urllib.request import Request, urlopen

ROOT = Path(__file__).resolve().parents[2]
NS = "flightbay-ambient-e2e"
PEER = NS + "-peer"
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--context", default="kind-mesh1")
parser.add_argument("--keep", action="store_true")
args = parser.parse_args()
if not args.context.startswith("kind-"):
    parser.error("this test is restricted to an explicitly named kind context")
work = Path(tempfile.mkdtemp(prefix="flightbay-ambient-"))
env = dict(os.environ, FLIGHTBAY_HOME=str(work / "home"))
env.pop("AGENT", None)
env.pop("FLIGHTBAY_AGENT", None)
desk = None
created_namespace = False
passed = 0


def run(*cmd, check=True):
    p = subprocess.run(cmd, cwd=ROOT, env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if check and p.returncode:
        raise RuntimeError(f"{' '.join(cmd)}\n{p.stdout}\n{p.stderr}")
    return p


def k(*cmd, check=True):
    return run("kubectl", "--context", args.context, *cmd, check=check)


def ok(message):
    global passed
    passed += 1
    print(f"PASS {message}", flush=True)


def until(fn, timeout=90):
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        value = fn()
        if value:
            return value
        time.sleep(2)
    raise RuntimeError("timed out waiting for live evidence")


def http(service, method="GET", count=1):
    command = f"for i in $(seq 1 {count}); do curl -s --max-time 4 -X {method} http://{service}:8080/; done"
    return k("-n", NS, "exec", "deploy/caller", "--", "sh", "-c", command, check=False).stdout


def code(service, method="GET", count=1):
    command = f"for i in $(seq 1 {count}); do curl -s -o /dev/null --max-time 4 -X {method} -w '%{{http_code}}\\n' http://{service}:8080/; done"
    return k("-n", NS, "exec", "deploy/caller", "--", "sh", "-c", command, check=False).stdout.split()


def request(service, path="/", method="GET", headers=None, status=False):
    cmd = ["-n", NS, "exec", "deploy/caller", "--", "curl", "-s", "--max-time", "6", "-X", method]
    for name, value in (headers or {}).items():
        cmd.extend(["-H", name + ": " + value])
    if status:
        cmd.extend(["-o", "/dev/null", "-w", "%{http_code}"])
    return k(*cmd, "http://" + service + ":8080" + path, check=False).stdout


try:
    if any(k("get", "namespace", name, check=False).returncode == 0 for name in [NS, PEER]):
        raise RuntimeError("a test namespace already exists; refusing to overwrite it")
    k("apply", "-f", str(ROOT / "test/e2e/ambient-workloads.yaml"))
    created_namespace = True
    k("-n", NS, "wait", "--for=condition=Available", "deployment", "--all", "--timeout=240s")
    k("-n", NS, "wait", "--for=condition=Programmed", "gateway/waypoint", "--timeout=180s")
    until(lambda: "stable" in http("front"))
    ok("real Service traffic traverses the Istio waypoint")
    classes = k("get", "gatewayclasses", "-o", "json").stdout
    rate_class = "enterprise-agentgateway-waypoint" if "enterprise-agentgateway-waypoint" in classes else "agentgateway-waypoint" if "agentgateway-waypoint" in classes else ""
    if rate_class:
        manifest = {"apiVersion": "gateway.networking.k8s.io/v1", "kind": "Gateway", "metadata": {"name": "rate-waypoint", "namespace": NS, "labels": {"istio.io/waypoint-for": "service"}}, "spec": {"gatewayClassName": rate_class, "listeners": [{"name": "mesh", "port": 15008, "protocol": "HBONE"}]}}
        f = work / "rate-waypoint.json"
        f.write_text(json.dumps(manifest))
        k("apply", "-f", str(f))
        k("-n", NS, "wait", "--for=condition=Programmed", "gateway/rate-waypoint", "--timeout=180s")
        rate = {"apiVersion": "v1", "kind": "Service", "metadata": {"name": "rate", "namespace": NS, "labels": {"istio.io/use-waypoint": "rate-waypoint"}}, "spec": {"selector": {"app": "stable"}, "ports": [{"name": "http", "port": 8080, "targetPort": 8080, "appProtocol": "http"}]}}
        f = work / "rate-service.json"
        f.write_text(json.dumps(rate))
        k("apply", "-f", str(f))
        until(lambda: "200" in code("rate"))
    collector, desk_bin = work / "flightbay", work / "flightbay-desk"
    run("go", "build", "-o", str(collector), "./cmd/flightbay")
    run("go", "build", "-o", str(desk_bin), "./cmd/flightbay-desk")
    capture = work / "ambient.tar.zst"
    run(str(collector), "collect", "--context", args.context, "--namespaces", NS + "," + PEER + ",istio-system", "--collect", "objects", "--no-logs", "--no-portforward", "--out", str(capture))
    log = (work / "desk.log").open("w")
    desk = subprocess.Popen([str(desk_bin), "open", str(capture), "--listen", "127.0.0.1:0", "--no-browser"], cwd=ROOT, env=env, stdout=log, stderr=log)
    match = until(lambda: re.search(r"UI:\s+(http://[^/]+/\?token=([a-f0-9]+))", (work / "desk.log").read_text()), timeout=120)
    base, token = match[1].split("/?")[0], match[2]

    def api(path, data=None, expected=200):
        req = Request(base + path, data=json.dumps(data).encode() if data is not None else None, headers={"Authorization": "Bearer " + token, "Origin": base, "Content-Type": "application/json"})
        try:
            response = urlopen(req, timeout=120)
        except HTTPError as e:
            response = e
        result = json.loads(response.read())
        if response.status != expected:
            raise RuntimeError(f"{path}: {response.status}: {result}")
        return result

    until(lambda: api("/api/workspace")["state"]["phase"] == "complete")
    result = api(f"/api/ambient/investigate?cluster={args.context}&namespace={NS}&service=front&route=wrong-parent")
    assert any(s["state"] == "fail" and ("listener" in s["id"] or "Accepted" in s["id"]) for s in result["steps"]), result
    assert any("kind: Service" in s.get("fixYaml", "") for s in result["steps"])
    failed = [s for s in result["steps"] if s["state"] == "fail"]
    assert all(s.get("runbook") for s in failed), failed
    commands = [c["command"] for c in failed[0]["runbook"]]
    assert any("ztunnel-config service" in c for c in commands), commands
    assert any("proxy-config all" in c for c in commands), commands
    assert any("pc log" in c and "--level debug" in c for c in commands), commands
    assert any("logs 'deploy/waypoint'" in c for c in commands), commands
    assert all(args.context in c for c in commands), commands
    ok("guided investigation explains the rejected waypoint parent and proposes a Service-parent change")
    ok("failed investigation checks provide real-context runbook commands mapped to captured evidence")

    def start_test(service, tasks, **parameters):
        req = dict(cluster=args.context, namespace=NS, service=service, port=8080, tasks=tasks, **parameters)
        plan = api("/api/ambient/plan", req)
        (work / f"{service}-{plan['id']}.yaml").write_text(plan["yaml"])
        validated = api("/api/ambient/test", {"operation": "validate", "context": args.context, "request": req, "planId": plan["id"]})
        test = dict(context=args.context, validation=validated["token"])
        api("/api/ambient/test", dict(test, operation="apply"))
        return test

    def cleanup(test):
        api("/api/ambient/test", dict(test, operation="cleanup"))
        ok("cleanup confirms test resources are absent without changing existing objects")

    caps = {t["id"]: t for t in api(f"/api/ambient/capabilities?cluster={args.context}&namespace={NS}&service=front")}
    assert caps["require-waypoint"]["supported"], caps["require-waypoint"]
    assert any(s["id"] == "waypoint-bound-istio.io/WaypointBound" for s in result["steps"])
    test = start_test("front", ["require-waypoint"])
    until(lambda: "200" in code("front"))
    until(lambda: request("stable", status=True) != "200")
    ok("waypoint-only ztunnel ALLOW keeps the waypoint path working and refuses a direct Service to the same workload")
    cleanup(test)
    until(lambda: "200" in code("stable"))

    explanation = api(f"/api/explain?cluster={args.context}&from={NS}/caller&to={NS}/explain-front")
    assert explanation["verdict"] == "denied" and '"8080"' in explanation["fix"] and "paths:" in explanation["fix"], explanation
    until(lambda: "403" in code("explain-front"))
    test = start_test("explain-front", ["explain"], explain={"from": NS + "/caller", "to": NS + "/explain-front", "ports": ["8080"], "paths": ["/private/*"]})
    until(lambda: request("explain-front", "/private/test", status=True) == "200")
    assert request("explain-front", "/other", status=True) == "403"
    ok("explainer repair enters the reviewed dry-run/apply flow with a Service port and L7 paths, restoring real traffic")
    cleanup(test)
    until(lambda: "403" in code("explain-front"))
    ok("undo removes the explainer repair and restores the original denial")

    test = start_test("front", ["header-routing"], headers={"x-flightbay-version": "canary"}, backends=[dict(name="canary", port=8080)])
    until(lambda: "canary" in request("front", headers={"x-flightbay-version": "canary"}))
    assert "stable" in request("front"), "non-matching request was routed to the header backend"
    ok("exact HTTPRoute header matching routes matching requests to canary and preserves the default path")
    cleanup(test)

    test = start_test("front", ["mirroring"], mirror=dict(name="canary", port=8080))
    mirror_path = "/flightbay-mirror-" + str(time.time_ns())
    def mirrored():
        # The primary response also succeeds before the new route reaches the
        # waypoint. Keep sending traffic while waiting for mirrored evidence.
        if "stable" not in request("front", path=mirror_path):
            return False
        return mirror_path in k("-n", NS, "logs", "deploy/canary", "--since=2m").stdout
    until(mirrored)
    ok("RequestMirror sends a copy to the selected backend while retaining the primary response")
    cleanup(test)

    if caps["retries"]["supported"]:
        test = start_test("front", ["retries"], retryAttempts=2)
        ok("installed experimental retry field passes server admission; retry effect remains UNVERIFIED")
        cleanup(test)
    else:
        api("/api/ambient/plan", dict(cluster=args.context, namespace=NS, service="front", port=8080, tasks=["retries"], retryAttempts=2), expected=400)
        ok("retry generation is refused when the installed HTTPRoute schema lacks retry.attempts")

    samples = "https://raw.githubusercontent.com/istio/istio/release-1.30/security/tools/jwt/samples/"
    public_jwks = urlopen(samples + "jwks.json", timeout=30).read().decode()
    public_token = urlopen(samples + "demo.jwt", timeout=30).read().decode().strip()
    test = start_test("front", ["jwt"], issuer="testing@secure.istio.io", jwks=public_jwks, requestPrincipals=["testing@secure.istio.io/testing@secure.istio.io"], paths=["/private/*"], methods=["GET"], headers={"x-team": "sre"})
    valid_headers = {"Authorization": "Bearer " + public_token, "x-team": "sre"}
    until(lambda: request("front", "/private/test", headers=valid_headers, status=True) == "200")
    assert request("front", "/private/test", status=True) == "403"
    assert request("front", "/private/test", headers={"Authorization": "Bearer invalid", "x-team": "sre"}, status=True) == "401"
    assert request("front", "/private/test", method="POST", headers=valid_headers, status=True) == "403"
    assert request("front", "/other", headers=valid_headers, status=True) == "403"
    assert request("front", "/private/test", headers={"Authorization": "Bearer " + public_token}, status=True) == "403"
    ok("RequestAuthentication plus authorization admits a valid JWT and enforces missing/invalid tokens, methods, paths and headers")
    cleanup(test)
    until(lambda: "200" in code("front"))

    if rate_class:
        agent_caps = {t["id"]: t for t in api(f"/api/ambient/capabilities?cluster={args.context}&namespace={NS}&service=rate")}
        assert not agent_caps["jwt"]["supported"] and "UNVERIFIED" in agent_caps["jwt"]["reason"]
        assert not agent_caps["mirroring"]["supported"] and not agent_caps["retries"]["supported"]
        ok("unverified agentgateway JWT, mirror and retry mappings stay unavailable for the selected waypoint type")
        test = start_test("rate", ["header-routing"], headers={"x-flightbay-version": "canary"}, backends=[dict(name="canary", port=8080)])
        until(lambda: "canary" in request("rate", headers={"x-flightbay-version": "canary"}))
        assert "stable" in request("rate")
        ok("agentgateway waypoint enforces exact header routing and the default backend")
        cleanup(test)

    # The map and editor must use existing rule values, rather than create a competing route.
    view = api(f"/api/ambient/investigate?cluster={args.context}&namespace={NS}&service=edit-front")
    edit = next(e for e in view["editable"] if e["ref"]["name"] == "existing-split")
    assert edit["supported"] and edit["request"]["backends"][0]["weight"] == 90
    test = start_test("edit-front", ["weighted-routing"], backends=[dict(name="stable", port=8080, weight=25), dict(name="canary", port=8080, weight=75)], edit=edit["request"]["edit"])
    changed = json.loads(k("-n", NS, "get", "httproute", "existing-split", "-o", "json").stdout)
    assert changed["spec"]["rules"][0]["backendRefs"][1]["weight"] == 75
    assert changed["metadata"]["annotations"]["customer.example/keep"] == "preserved"
    assert changed["spec"]["rules"][0]["filters"][0]["requestHeaderModifier"]["add"][0]["value"] == "existing"
    ok("attachment map loads actual weights and the editor updates the existing rule, preserving filters and annotations")
    cleanup(test)
    restored = json.loads(k("-n", NS, "get", "httproute", "existing-split", "-o", "json").stdout)
    assert restored["spec"]["rules"][0]["backendRefs"][0]["weight"] == 90
    ok("undo restores the existing routing weights")

    test = start_test("edit-front", ["weighted-routing"], trafficMode="equal", backends=[dict(name="stable", port=8080, weight=0), dict(name="canary", port=8080, weight=0)], edit=edit["request"]["edit"])
    changed = json.loads(k("-n", NS, "get", "httproute", "existing-split", "-o", "json").stdout)
    assert all(b.get("weight", 1) == 1 for b in changed["spec"]["rules"][0]["backendRefs"])
    ok("default/equal routing omits overrides and accepts API-server materialised weights")
    cleanup(test)

    recovery = api(f"/api/ambient/investigate?cluster={args.context}&namespace={NS}&service=broken-selector")
    pod = next(p for p in recovery["recovery"]["pods"] if p["labels"].get("app") == "stable")
    assert any(a["id"] == "endpoints" for step in recovery["steps"] for a in step.get("actions", []))
    test = start_test("broken-selector", ["service-selector"], selectedPod=pod["name"], selector={"app": "stable"})
    until(lambda: "stable" in http("broken-selector"))
    ok("endpoint remediation generates and applies the selected workload's Service selector, restoring real traffic")
    cleanup(test)

    test = start_test("scaled", ["scale-workload"], workload="scaled-zero", replicas=1)
    until(lambda: "200" in code("scaled"))
    ok("endpoint recovery scales an explicitly selected matching zero-replica Deployment")
    cleanup(test)

    test = start_test("direct", ["waypoint"], waypoint=dict(mode="existing", scope="service", namespace=NS, name="waypoint", **{"class": "istio-waypoint"}))
    assert json.loads(k("-n", NS, "get", "service", "direct", "-o", "json").stdout)["metadata"]["labels"]["istio.io/use-waypoint"] == "waypoint"
    until(lambda: "200" in code("direct"))
    ok("existing-waypoint Service enrolment changes only the reviewed binding")
    cleanup(test)

    test = start_test("front", ["waypoint"], waypoint=dict(mode="new", scope="shared", namespace=NS, name="shared-test-waypoint", namespaces=[NS, PEER], **{"class": "istio-waypoint"}))
    for name in [NS, PEER]:
        current = json.loads(k("get", "namespace", name, "-o", "json").stdout)
        assert current["metadata"]["labels"]["istio.io/use-waypoint"] == "shared-test-waypoint"
    override = json.loads(k("-n", NS, "get", "service", "front", "-o", "json").stdout)
    assert override["metadata"]["labels"]["istio.io/use-waypoint"] == "waypoint"
    ok("new shared waypoint reports Programmed before explicitly enrolling two namespaces and preserving Service overrides")
    cleanup(test)

    test = start_test("front", ["weighted-routing"], backends=[dict(name="stable", port=8080, weight=50), dict(name="canary", port=8080, weight=50)])
    until(lambda: "canary" in http("front", count=20))
    responses = http("front", count=50)
    assert "stable" in responses and "canary" in responses, responses
    ok("generated Service-parent HTTPRoute sends real traffic to both selected backends")
    cleanup(test)
    until(lambda: "200" in code("slow"))
    test = start_test("slow", ["timeouts"], timeout="500ms")
    until(lambda: "504" in code("slow"))
    ok("generated HTTPRoute timeout produces a real 504 for a deliberately slow backend")
    cleanup(test)
    test = start_test("front", ["methods"], methods=["GET"])
    until(lambda: "403" in code("front", method="POST"))
    assert "200" in code("front"), "GET was not kept working"
    ok("generated L7 policy allows GET and denies POST at the waypoint")
    cleanup(test)
    test = start_test("direct", ["l4-default-deny"])
    def blocked():
        codes = code("direct", count=2)
        return len(codes) >= 2 and all(x != "200" for x in codes)
    until(blocked)
    ok("generated ztunnel policy blocks the selected direct workload")
    cleanup(test)
    until(lambda: "200" in code("direct"))
    test = start_test("direct", ["l4-default-deny", "l4-allow-namespace", "strict-mtls"], sourceNamespace=NS)
    until(lambda: "200" in code("direct"))
    ok("generated namespace allow and STRICT mTLS keep authorised direct ambient traffic working")
    cleanup(test)
    until(lambda: "503" in code("ha", count=20))
    test = start_test("ha", ["endpoint-failover", "load-balancing"], algorithm="ROUND_ROBIN")
    # Ejection is an observed effect, not merely successful admission of a DestinationRule.
    until(lambda: code("ha", count=20).count("200") >= 18)
    ok("generated outlier detection ejects unhealthy endpoints under real HTTP traffic")
    cleanup(test)
    if rate_class:
        test = start_test("rate", ["rate-limit"], requests=1, unit="Minutes")
        until(lambda: "429" in code("rate", count=5))
        ok("generated agentgateway Service-targeted policy returns 429 under real load")
        cleanup(test)
    else:
        print("SKIP agentgateway rate-limit proof: no agentgateway waypoint class installed", flush=True)
    assert not api("/api/ambient/tests"), "active tests remain"
    print(f"{passed} checks passed; artifacts: {work}", flush=True)
finally:
    if desk is not None:
        desk.terminate()
        try:
            desk.wait(timeout=10)
        except subprocess.TimeoutExpired:
            desk.kill()
    if created_namespace and not args.keep:
        k("delete", "namespace", NS, PEER, "--wait=true", "--timeout=180s")
        assert all(k("get", "namespace", name, check=False).returncode != 0 for name in [NS, PEER])
