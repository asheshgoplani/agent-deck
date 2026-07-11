---
title: Cartrack Fleet API — Reference
source_spec: https://developer.cartrack.com/openapi/openapi.yaml
spec_version: 1.26.0622.1
generated: 2026-07-11
---

# Cartrack Fleet API — Reference

> The Cartrack Fleet API provides fleet information directly into your systems, allowing you to manage your fleet straight from your own applications. This API is designed to be simple to use and easy to integrate with your existing systems. It provides access to a wide range of fleet information, including vehicle details, driver information, and trip data. The API is secured using basic authentication, and all requests must be made over HTTPS. Please ensure you are using this API with the right base URL.

This is an offline reference for the **Cartrack Fleet API**, generated from the canonical [OpenAPI 3.1 spec](https://developer.cartrack.com/openapi/openapi.yaml) (v1.26.0622.1) and the [developer docs](https://developer.cartrack.com/docs/fleet-api-general/overview). It covers **43 endpoint categories** and **256 operations**.

## Why this exists

The live endpoint pages render request/response schemas client-side (Redocly OpenAPI plugin), so the server HTML omits the request body and parameters. This reference is built directly from the published OpenAPI spec so the detail is complete and accurate offline.

## Getting started

1. **Authentication** — HTTP Basic Auth. Send an `Authorization: Basic <base64(username:password)>` header over HTTPS. See [Authentication](general/authentication.md) for admin vs. user credential generation.
2. **Base URL** — pick the regional endpoint matching your Cartrack account. See the table below; full notes in [Base URLs](general/base-url.md).
3. **Rate limits** — default **1,000 requests/minute**; some endpoints are stricter (10/min). Exceeding returns `429`. See [Rate Limiting](general/rate-limiting.md).
4. **First request** — [Quick Start](general/quick-start.md).
5. **Async updates** — [Webhook Notifications](general/webhook-notification.md).

### Regional base URLs

| Region | Base URL |
|---|---|
| Australia | `https://fleetapi-au.cartrack.com/rest` |
| Botswana | `https://fleetapi-bw.cartrack.com/rest` |
| Spain | `https://fleetapi-es.cartrack.com/rest` |
| Hong Kong | `https://fleetapi-hk.cartrack.com/rest` |
| Indonesia | `https://fleetapi-id.cartrack.com/rest` |
| Kenya | `https://fleetapi-ke.karooooo.com/rest` |
| Cambodia | `https://fleetapi-kh.cartrack.com/rest` |
| Middle East | `https://fleetapi-me.cartrack.com/rest` |
| Malawi | `https://fleetapi-mw.cartrack.com/rest` |
| Malaysia | `https://fleetapi-my.cartrack.com/rest` |
| Mozambique | `https://fleetapi-mz.cartrack.com/rest` |
| Namibia | `https://fleetapi-na.cartrack.com/rest` |
| Nigeria | `https://fleetapi-ng.cartrack.com/rest` |
| New Zealand | `https://fleetapi-nz.cartrack.com/rest` |
| Philippines | `https://fleetapi-ph.cartrack.com/rest` |
| Poland | `https://fleetapi-pl.cartrack.com/rest` |
| Portugal | `https://fleetapi-pt.cartrack.com/rest` |
| Rwanda | `https://fleetapi-rw.cartrack.com/rest` |
| Kingdom of Saudi Arabia | `https://fleetapi-sa.karooooo.com/rest` |
| Singapore | `https://fleetapi-sg.cartrack.com/rest` |
| Swaziland | `https://fleetapi-sw.cartrack.com/rest` |
| Thailand | `https://fleetapi-th.cartrack.com/rest` |
| Tanzania | `https://fleetapi-tz.cartrack.com/rest` |
| Vietnam | `https://fleetapi-vn.cartrack.com/rest` |
| South Africa | `https://fleetapi-za.cartrack.com/rest` |
| Zambia | `https://fleetapi-zm.cartrack.com/rest` |
| Zanzibar | `https://fleetapi-znz.cartrack.com/rest` |
| Zimbabwe | `https://fleetapi-zw.cartrack.com/rest` |

All paths in the endpoint reference are appended to your regional base URL (e.g. `https://fleetapi-za.cartrack.com/rest` + `/alerts/ignition`).

## How-to & concepts

- [Overview](general/overview.md)
- [Use Cases](general/use-cases.md)
- [Authentication](general/authentication.md)
- [Base URLs](general/base-url.md)
- [Quick Start](general/quick-start.md)
- [Rate Limiting](general/rate-limiting.md)
- [Webhook Notification](general/webhook-notification.md)
- [Application registration](applications/fleet-api.md)
- [Changelog](general/changelog.md)
- Service category overviews: [delivery jobs](general/services/delivery-job-services.md), [driver identification](general/services/driver-identification-services.md), [fuel](general/services/fuel-services.md), [mileage/odometer](general/services/mileage-odometer-services.md), [positions/route](general/services/positions-route-services.md), [vehicle sensors](general/services/vehicle-sensors-services.md), [vehicle temperature](general/services/vehicle-temperature-services.md), [vehicles/events](general/services/vehicles-events-services.md), [vision](general/services/vision-services.md)

## Endpoint reference

One file per tag. Each documents every operation with method, path, parameters, request body, responses, and examples.

| Category | Operations | File |
|---|---:|---|
| AEMP ISO15143-3 | 2 | [endpoints/aemp-iso15143-3.md](endpoints/aemp-iso15143-3.md) |
| Alerts | 7 | [endpoints/alerts.md](endpoints/alerts.md) |
| Car Manufacturers | 1 | [endpoints/car-manufacturers.md](endpoints/car-manufacturers.md) |
| CarWatch | 2 | [endpoints/carwatch.md](endpoints/carwatch.md) |
| Coaching | 1 | [endpoints/coaching.md](endpoints/coaching.md) |
| Delivery Countries | 1 | [endpoints/delivery-countries.md](endpoints/delivery-countries.md) |
| Delivery Customers | 5 | [endpoints/delivery-customers.md](endpoints/delivery-customers.md) |
| Delivery Drivers | 6 | [endpoints/delivery-drivers.md](endpoints/delivery-drivers.md) |
| Delivery Jobs | 9 | [endpoints/delivery-jobs.md](endpoints/delivery-jobs.md) |
| Delivery Plans | 2 | [endpoints/delivery-plans.md](endpoints/delivery-plans.md) |
| Delivery Reports | 1 | [endpoints/delivery-reports.md](endpoints/delivery-reports.md) |
| Delivery Special Equipment | 1 | [endpoints/delivery-special-equipment.md](endpoints/delivery-special-equipment.md) |
| Driver Groups | 8 | [endpoints/driver-groups.md](endpoints/driver-groups.md) |
| Drivers | 8 | [endpoints/drivers.md](endpoints/drivers.md) |
| Fitments | 1 | [endpoints/fitments.md](endpoints/fitments.md) |
| Fuel | 7 | [endpoints/fuel.md](endpoints/fuel.md) |
| Generator | 1 | [endpoints/generator.md](endpoints/generator.md) |
| Geofence Groups | 6 | [endpoints/geofence-groups.md](endpoints/geofence-groups.md) |
| Geofences | 9 | [endpoints/geofences.md](endpoints/geofences.md) |
| Helpdesk | 1 | [endpoints/helpdesk.md](endpoints/helpdesk.md) |
| Leads | 4 | [endpoints/leads.md](endpoints/leads.md) |
| Maintenance | 5 | [endpoints/maintenance.md](endpoints/maintenance.md) |
| MiFleet | 92 | [endpoints/mifleet.md](endpoints/mifleet.md) |
| Mikey | 3 | [endpoints/mikey.md](endpoints/mikey.md) |
| Notifications | 1 | [endpoints/notifications.md](endpoints/notifications.md) |
| Points of interest | 5 | [endpoints/points-of-interest.md](endpoints/points-of-interest.md) |
| Reminders | 1 | [endpoints/reminders.md](endpoints/reminders.md) |
| Road User Charge (RUC) | 1 | [endpoints/road-user-charge-ruc.md](endpoints/road-user-charge-ruc.md) |
| Subusers | 4 | [endpoints/subusers.md](endpoints/subusers.md) |
| System | 1 | [endpoints/system.md](endpoints/system.md) |
| Tachograph | 3 | [endpoints/tachograph.md](endpoints/tachograph.md) |
| Terminal Commands | 4 | [endpoints/terminal-commands.md](endpoints/terminal-commands.md) |
| Topics | 2 | [endpoints/topics.md](endpoints/topics.md) |
| Trips | 4 | [endpoints/trips.md](endpoints/trips.md) |
| Vehicle | 13 | [endpoints/vehicle.md](endpoints/vehicle.md) |
| Vehicle (Electric) | 8 | [endpoints/vehicle-electric.md](endpoints/vehicle-electric.md) |
| Vehicle Commands | 4 | [endpoints/vehicle-commands.md](endpoints/vehicle-commands.md) |
| Vehicle Driver Linkage | 4 | [endpoints/vehicle-driver-linkage.md](endpoints/vehicle-driver-linkage.md) |
| Vehicle Events | 4 | [endpoints/vehicle-events.md](endpoints/vehicle-events.md) |
| Vehicle Groups | 6 | [endpoints/vehicle-groups.md](endpoints/vehicle-groups.md) |
| Vehicle Status | 1 | [endpoints/vehicle-status.md](endpoints/vehicle-status.md) |
| Vision | 6 | [endpoints/vision.md](endpoints/vision.md) |
| internal | 1 | [endpoints/internal.md](endpoints/internal.md) |
| **Shared schemas** | — | [endpoints/_schemas.md](endpoints/_schemas.md) |

## Scope

Includes the **Fleet API** only. Excluded (separate Cartrack products): Mikey iOS/Android BLE SDKs, mobile SDKs, and telematics data ingestion. Add them by extending the crawler in `scripts/providers/cartrack/crawl_cartrack.py` if needed.

## Regeneration

These docs are generated, not hand-maintained. Scripts live in `scripts/providers/cartrack/`.

```bash
# one-time: tooling (html2text + pyyaml)
python3 -m venv .venv && .venv/bin/pip install -q html2text pyyaml

# fetch spec + sitemap into scripts/providers/cartrack/
curl -sL https://developer.cartrack.com/openapi/openapi.yaml -o scripts/providers/cartrack/openapi.yaml
curl -sL https://developer.cartrack.com/sitemap.xml | grep -oE '<loc>[^<]+</loc>' | sed 's/<[^>]*>//g' | grep -E '/docs/' | grep -v '/changelog/' > scripts/providers/cartrack/sitemap_urls.txt

# generate (run from repo root)
.venv/bin/python scripts/providers/cartrack/crawl_cartrack.py     # how-to pages
.venv/bin/python scripts/providers/cartrack/gen_openapi_docs.py   # endpoint docs
.venv/bin/python scripts/providers/cartrack/gen_readme.py         # this index
```
