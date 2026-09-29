---
title: SQL
weight: 140
description: Query the workspace's logs, objects, events and flows with read-only DuckDB SQL.
---

# SQL

The workspace is a DuckDB database, and you can query it directly. Use SQL for the questions the search boxes cannot answer: counts, grouping, joins between logs and objects, percentiles, top-N lists.

Queries are read-only. They cannot change the workspace.

## Running a query

```bash
fbsql "SELECT level, count(*) FROM logs GROUP BY ALL ORDER BY 2 DESC"
```

```text
level  count_star()
info   42797
error  1560
warn   1192
-- 3 rows in 1 ms
```

With no query, `fbsql` starts a prompt. End each statement with `;`. `help` lists the tables, `exit` leaves.

```text
$ fbsql
Flightbay SQL (read-only DuckDB). End statements with ; and type help or exit.
fbsql> SELECT kind, count(*) FROM objects
   ... GROUP BY ALL ORDER BY 2 DESC LIMIT 5;
```

`--limit` caps the rows printed (200 by default); the footer says when there were more. The MCP `run_sql` tool runs the same queries for agents.

## Tables

### logs

A view over `log_lines` with a timestamp column named `time`. Use it for queries that filter or group by time.

| Column | Type | Notes |
|---|---|---|
| `id` | BIGINT | Line ID, as used by the log explorer and `log_context` |
| `time` | TIMESTAMP | The kubelet timestamp, UTC |
| `cluster`, `namespace`, `pod`, `container` | VARCHAR | Where it came from |
| `workload_kind`, `workload` | VARCHAR | The Deployment, DaemonSet, StatefulSet or Job the pod belongs to |
| `stream` | VARCHAR | `current` or `previous` |
| `node` | VARCHAR | Node the pod ran on |
| `level` | VARCHAR | `error`, `warn`, `info`, `debug` or `trace` |
| `scope` | VARCHAR | Logger scope |
| `msg` | VARCHAR | The message (decoded, for JSON lines) |
| `fields` | MAP(VARCHAR, VARCHAR) | Structured fields parsed from the line: `fields['trace_id']` |
| `raw` | VARCHAR | The line as captured (redacted) |

### log_lines

The underlying table: the same columns, with `ts` as Unix nanoseconds instead of `time`, plus `seq` (line number within its stream) and `chunk` (the bundle file it came from).

### objects

| Column | Type | Notes |
|---|---|---|
| `cluster` | VARCHAR | |
| `api_group`, `version`, `resource`, `kind` | VARCHAR | `api_group` is `''` for core types |
| `namespace`, `name` | VARCHAR | |
| `labels` | MAP(VARCHAR, VARCHAR) | `labels['app']` |
| `object` | VARCHAR | The whole object as JSON text: `object->>'$.status.phase'` |

If deferred objects are loaded later, an object can appear twice; the newest row wins in the UI.

### events

| Column | Type |
|---|---|
| `cluster`, `namespace`, `type`, `reason`, `object_kind`, `object_name`, `message` | VARCHAR |
| `count` | BIGINT |
| `last_seen` | VARCHAR |
| `event` | VARCHAR (the whole event as JSON text) |

### flows

Observed traffic parsed from Envoy and CoreDNS logs, and imported flow logs. One row per record; sum `count` when grouping.

| Column | Notes |
|---|---|
| `cluster`, `source` | `source` is `envoy`, `coredns`, `hubble`, `calico`, `aws-vpc`, `gcp-vpc`, `azure-vnet` or `generic` |
| `src_ip`, `src_namespace`, `src_pod`, `src_identity` | The caller |
| `dst_ip`, `dst_port`, `dst_host`, `dst_namespace`, `dst_workload` | The destination |
| `protocol`, `method`, `path`, `tool`, `route`, `via` | What was called, and through what |
| `verdict`, `direction` | Allowed or denied, and which way |
| `count` | Records this row stands for |
| `file`, `origin` | The flow file, and whether it came from `logs`, the `bundle` or the `reviewer` |

ztunnel and agentgateway flows are read from the logs directly and are not in this table. Use `fbflows` for those, or query `logs` with `fields['src.workload']` and similar.

## Examples

```sql
-- Errors per pod
SELECT namespace, pod, count(*) AS errors
FROM logs WHERE level = 'error'
GROUP BY ALL ORDER BY errors DESC LIMIT 10;

-- Pod phases
SELECT namespace, name, object->>'$.status.phase' AS phase
FROM objects WHERE kind = 'Pod';

-- Warning events
SELECT reason, object_kind, object_name, message
FROM events WHERE type = 'Warning';

-- Requests per trace
SELECT fields['trace_id'] AS trace, count(*)
FROM logs WHERE fields['trace_id'] IS NOT NULL
GROUP BY ALL ORDER BY 2 DESC;

-- Errors in the last 15 minutes of the capture, per namespace
SELECT namespace, count(*)
FROM logs
WHERE level = 'error' AND time > (SELECT max(time) FROM logs) - INTERVAL 15 MINUTE
GROUP BY ALL;

-- Objects per kind
SELECT kind, count(*) FROM objects GROUP BY ALL ORDER BY 2 DESC;

-- Who called what, from imported flows
SELECT source, src_pod, dst_host, dst_port, sum(count) AS n
FROM flows GROUP BY ALL ORDER BY n DESC;

-- What columns does a table have?
DESCRIBE flows;
```

## Read-only rules

- One statement per query. A `;` in the middle is refused.
- The statement must start with `SELECT`, `WITH`, `FROM`, `DESCRIBE`, `SHOW`, `SUMMARIZE`, `EXPLAIN`, `VALUES`, `TABLE`, `PIVOT` or `UNPIVOT`. Anything else returns "only read-only queries are allowed".
- Every query runs inside a transaction that is rolled back afterwards.
- A query is stopped after two minutes.

## Over HTTP

`GET /api/sql?q=...&limit=...` on the desk, or `POST /api/sql` with `{"query": "SELECT ..."}`, returns `columns`, `rows`, `truncated` and `ms` as JSON. Send the desk token as `Authorization: Bearer`. In the terminal, `$FLIGHTBAY_URL` and `$FLIGHTBAY_TOKEN` are set:

```bash
curl -s -H "Authorization: Bearer $FLIGHTBAY_TOKEN" \
  --get --data-urlencode "q=SELECT count(*) FROM logs" "$FLIGHTBAY_URL/api/sql"
```
