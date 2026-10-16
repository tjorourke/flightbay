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


try:
    if k("get", "namespace", NS, check=False).returncode == 0:
        raise RuntimeError(f"namespace {NS} already exists; refusing to overwrite it")
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
    run(str(collector), "collect", "--context", args.context, "--namespaces", NS, "--collect", "objects", "--no-logs", "--no-portforward", "--out", str(capture))
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
    ok("guided investigation explains the rejected waypoint parent and proposes a Service-parent change")

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
        k("delete", "namespace", NS, "--wait=true", "--timeout=180s")
        assert k("get", "namespace", NS, check=False).returncode != 0
