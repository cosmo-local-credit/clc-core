# OpenTelemetry collector

clc-core pushes logs (flushed every 5s) and a few metrics (every 5 minutes) over
OTLP/HTTP to an OpenTelemetry Collector, which forwards them to Uptrace.

```
clc-core ──OTLP/HTTP :4318──▶ otel-collector ──OTLP/HTTP──▶ Uptrace
```

Export is opt-in. The app does nothing unless `OTEL_EXPORTER_OTLP_ENDPOINT` is
set; stderr logging (`docker logs`) is unchanged either way.

## Files

| File | Purpose |
| --- | --- |
| `collector-config.yaml` | Collector pipelines: OTLP in, Uptrace out. |
| `otel.env.example` | Template for `otel.env`, which holds the Uptrace DSN. |

The Uptrace DSN is a secret. It lives only in `otel.env` on the host, which
`.gitignore` excludes (`**/*.env`). Never commit it.

## What clc-core sends

- **Logs**: every `slog` record at the level stderr uses, so `DEBUG` and `DEV`
  apply to both.
- **Metrics**: Go runtime stats, DB pool usage (`db.client.connection.*`) and an
  `http.server.request.duration` histogram.

Standard `OTEL_*` variables are honoured, e.g. `OTEL_RESOURCE_ATTRIBUTES`
and `OTEL_METRIC_EXPORT_INTERVAL` (milliseconds, default 5 minutes).

## Deploying next to clc-core (docker compose)

1. Copy `collector-config.yaml` to the compose directory, and create `otel.env`
   from `otel.env.example` with the real DSN (`chmod 600`).
2. Add the collector, and point clc-core at it:

```yaml
services:
  clc-core:
    environment:
      OTEL_EXPORTER_OTLP_ENDPOINT: http://otel-collector:4318
      OTEL_RESOURCE_ATTRIBUTES: deployment.environment.name=staging

  # Internal only: no published ports. The contrib build is needed for the
  # resource_detection and cumulative_to_delta processors. Pinned and
  # unlabelled so Watchtower leaves it alone; bump deliberately.
  otel-collector:
    image: otel/opentelemetry-collector-contrib:0.162.0
    restart: unless-stopped
    command: ["--config=/etc/otelcol/config.yaml"]
    env_file: otel.env
    volumes:
      - ./collector-config.yaml:/etc/otelcol/config.yaml:ro
    networks:
      - internal
```

`otel-collector` must share a network with `clc-core`.

## Notes

- The collector exports over OTLP/HTTP rather than gRPC. The gRPC client logs a
  warning per unreachable IPv6 address on every reconnect, and Docker bridge
  networks have no IPv6.
- A `traces` pipeline is configured although clc-core emits no traces yet, so
  adding a tracer later needs no collector change.
- Check it is flowing in Uptrace with `where service_name = 'clc-core'`.
