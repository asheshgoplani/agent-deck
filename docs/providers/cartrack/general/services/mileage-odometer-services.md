---
source: https://developer.cartrack.com/docs/fleet-api-general/services/mileage-odometer-services
title: Mileage and Odometer Services
---

# Mileage and Odometer Services

# Mileage and Odometer Services

This page explains how vehicle mileage and odometer data is tracked and reported through the Fleet API, covering how values are sourced, which endpoints to use for different use cases, and common integration pitfalls.

## Which Endpoint Should I Use?​

Use this mapping based on what you want to accomplish:

Goal| Endpoint
---|---
Get total distance travelled over a period| [Get Odometer Reading](<https://developer.cartrack.com/docs/fleet-api/get-the-odometer-reading>)
Get a breakdown of distance per trip| [Get All Trips](<https://developer.cartrack.com/docs/fleet-api/get-all-trips>) / [Get Trips by Registration](<https://developer.cartrack.com/docs/fleet-api/get-trips-by-registration>)
Get the current odometer snapshot for a vehicle| [Get Vehicles Status](<https://developer.cartrack.com/docs/fleet-api/get-vehicles-status-location-fuel-odometer-and-more>)

## 1) How Mileage is Tracked​

### Data Source​

Cartrack trackers read odometer data directly from the vehicle's CAN bus where the hardware and vehicle support it. CAN bus odometer data reflects the vehicle's own internal mileage counter, making it the most accurate source.

If CAN bus odometer data is not available for a vehicle, Cartrack calculates distance from GPS positions instead. The calculation method is transparent to you in the API — the fields are the same regardless of source.

### Units​

All odometer and distance values in the Fleet API are returned in **meters** unless otherwise stated. The vehicle status endpoint (`GET /vehicles/status`) is the only exception — it supports an optional `?odometer_in_km=true` parameter to return the value in kilometers.

### Flags to Watch​

Two flags on the odometer endpoint can indicate that values may be inconsistent over a queried period:

Flag| Meaning
---|---
`odometer_reset`| The vehicle's odometer was reset during the period. This can happen when the tracker is reconfigured from GPS odometer to CAN odometer, or when the odometer is manually reset. Distance calculations across a reset period may not be reliable.
`terminal_has_changed`| The physical Cartrack tracker in the vehicle was replaced during the period. The odometer counter restarts from a new baseline on the replacement device.

If either flag is `true` for a queried period, treat the distance values with caution and consider splitting the query into periods before and after the event.

## 2) Distance for a Period​

### What It Means​

`GET /vehicles/{registration}/odometer` returns the odometer reading at the start and end of a specified time range, plus the total distance travelled in between. This is the recommended endpoint for any use case that needs accurate period-level distance, such as daily mileage reports or billing.

### Response Fields​

Field| Description
---|---
`start_odometer_value`| Odometer at the start of the queried period, in meters
`end_odometer_value`| Odometer at the end of the queried period, in meters
`distance`| Total distance travelled during the period, in meters
`current_odometer_value`| The vehicle's current odometer reading at query time, in meters
`latest_event_ts`| Timestamp of the most recent data received from the vehicle

### Network Coverage Note​

The `latest_event_ts` field indicates when Cartrack last received data from the vehicle. If the vehicle is operating in an area with poor network coverage, data transmission may be delayed. If `latest_event_ts` is significantly earlier than your `end_timestamp`, the returned values may not yet reflect the full period. Query again later once coverage is restored.

### Developer Considerations​

  * The time range is limited to a maximum of 31 days per request.
  * Check `odometer_reset` and `terminal_has_changed` before relying on the `distance` value for a period.
  * If you need distance across more than 31 days, issue multiple requests and sum the `distance` values for periods where neither flag is `true`.

## 3) Per-Trip Distance​

### What It Means​

The trips endpoints return individual trip records, each with a `trip_distance` field and odometer readings at the start and end of the trip. This is useful when you need a journey-level breakdown rather than a period total.

### Response Fields (per trip)​

Field| Description
---|---
`trip_distance`| Distance covered during the trip, in meters
`start_odometer`| Odometer at the beginning of the trip, in meters
`end_odometer`| Odometer at the end of the trip, in meters

### The Trip Boundary Pitfall​

Do not sum trip_distance for daily totals

The trips endpoint returns any trip that overlaps the requested time window, including trips that started before `start_timestamp` or ended after `end_timestamp`. Those trips are returned in full — their full distance is included, not just the portion that falls within your window. Summing `trip_distance` across all returned trips will over- or under-count distance relative to your requested period.

**For accurate daily or period distance totals, always use`GET /vehicles/{registration}/odometer` instead.**

### Developer Considerations​

  * Use trips data for per-journey breakdowns, driving behaviour reports, and start/end location analysis.
  * Do not derive total period distance by summing `trip_distance` — use the odometer endpoint.
  * The trips endpoint supports up to 31 days per request and returns paginated results.

## 4) Current Odometer Snapshot​

`GET /vehicles/status` includes an `odometer` field representing the vehicle's odometer reading as of its last update event. This is a point-in-time snapshot, not a period calculation. Use it for live fleet dashboards and current-state displays.

Pass `?odometer_in_km=true` to receive the value in kilometers instead of meters.

## Recommended Integration Pattern​

  1. For **daily or period distance reporting** : use `GET /vehicles/{registration}/odometer` with `start_timestamp` and `end_timestamp` set to midnight boundaries. Check `odometer_reset` and `terminal_has_changed` before using the `distance` value.
  2. For **per-trip mileage breakdown** : use `GET /trips/{registration}` and rely on `trip_distance` per individual trip record, not on a sum across trips.
  3. For **live fleet odometer display** : poll `GET /vehicles/status` at your refresh interval and read the `odometer` field.
  4. If values look inconsistent for a period, query `latest_event_ts` from the odometer endpoint to confirm whether all data has been received — coverage gaps can delay transmission.
