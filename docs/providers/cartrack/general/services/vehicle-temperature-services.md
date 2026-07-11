---
source: https://developer.cartrack.com/docs/fleet-api-general/services/vehicle-temperature-services
title: Vehicle Temperature Services
---

# Vehicle Temperature Services

# Vehicle Temperature Services

This page explains how vehicle temperature data is reported through the Fleet API, covering the available temperature sources, which endpoints to use for different use cases, and how temperature alerts are delivered.

## Temperature Sources​

The Fleet API exposes two categories of temperature data:

Category| Fields| Where it comes from
---|---|---
Cargo / cabin temperature probes| `temp1`, `temp2`, `temp3`, `temp4`| Up to four external temperature sensors wired to the Cartrack device, commonly used for refrigerated transport and cold chain monitoring
Engine and unit temperatures| `water_temp` (coolant), `oil_temp`, `unit_temp`| Read from the vehicle's CAN bus (coolant and oil) or from the Cartrack device itself (unit temperature)

All temperature values are returned in **Celsius** as decimal numbers. A field is `null` when the corresponding sensor is not fitted or not reporting.

warning

Temperature sensor availability depends on the hardware installed in the vehicle. If no temperature probes are fitted, the `temp1` to `temp4` fields return `null`. Contact your Cartrack account manager if you are unsure which sensors are installed on your fleet.

## Which Endpoint Should I Use?​

Use this mapping based on what you want to accomplish:

Goal| Endpoint
---|---
Get the latest temperature reading for all vehicles| [Get Vehicles Temperature Data](<https://developer.cartrack.com/docs/fleet-api/get-vehicles-temperature-data>)
Get historical temperature readings for a period| [Get Vehicles Temperature Data](<https://developer.cartrack.com/docs/fleet-api/get-vehicles-temperature-data>)
Get temperature together with position, fuel, and odometer in one call| [Get Vehicles Status](<https://developer.cartrack.com/docs/fleet-api/get-vehicles-status-location-fuel-odometer-and-more>)
Get engine coolant and oil temperature per event| [Get Events for All Vehicles](<https://developer.cartrack.com/docs/fleet-api/get-events-for-all-vehicles>) / [Get Events for One Vehicle](<https://developer.cartrack.com/docs/fleet-api/get-events-for-one-vehicle>)
Retrieve triggered temperature alert notifications| [Get Alerts Notifications](<https://developer.cartrack.com/docs/fleet-api/get-alerts-notifications>)

## 1) Dedicated Temperature Endpoint​

### What It Means​

`GET /topics/vehicles/temperature` is the recommended endpoint for temperature monitoring. It returns the readings from the four temperature probes for all vehicles in the account, and operates in two modes:

  * **Latest mode** : omit both `filter[start_timestamp]` and `filter[end_timestamp]` to get the most recent temperature reading per vehicle, provided data was reported within the last 2 months.
  * **History mode** : provide a time range to get all readings within it. The range is limited to a maximum **24-hour** period. If only one timestamp is given, the other defaults to 24 hours before or after it.

Use `filter[registration]` to restrict the response to a single vehicle.

### Response Fields​

Field| Type| Description
---|---|---
`vehicle_id`| integer| Cartrack vehicle identifier
`registration`| string| The vehicle's registration
`temp1` to `temp4`| number or null| Temperature reading from sensors 1 to 4, in Celsius
`event_ts`| string| When the reading was recorded by the device
`recieved_ts`| string| When the reading was received by Cartrack

If `recieved_ts` is significantly later than `event_ts`, the vehicle was likely in an area with poor network coverage and the data was transmitted with a delay. Keep this in mind when monitoring time-sensitive cold chain thresholds.

### Sub-User Access​

This endpoint supports Topic-Based Access Control. Grant the `TEMPERATURE` topic to a sub-user to give it the same temperature data access as the account administrator, without exposing the rest of the account.

### Developer Considerations​

  * History queries are limited to 24 hours per request. For longer periods, issue multiple requests with consecutive windows.
  * Results are paginated: use the `page` and `limit` parameters to iterate through large fleets.
  * In latest mode, vehicles that have not reported temperature data in the last 2 months are not returned.

## 2) Temperature in Vehicle Status​

`GET /vehicles/status` includes the `temp1` to `temp4` fields as part of each vehicle's last known state, alongside position, ignition, fuel, and odometer. This is a point-in-time snapshot, not a history.

Use it when you already poll vehicle status for a live dashboard and want to display current temperatures without an extra API call.

## 3) Engine Temperatures via Events​

The events endpoints (`GET /vehicles/events` and `GET /vehicles/{registration}/events`) return the full event stream from the vehicle. Each event includes the probe readings plus the engine-related temperatures:

Field| Description
---|---
`temp1` to `temp4`| Temperature probes 1 to 4, in Celsius
`water_temp`| Engine coolant temperature, in Celsius
`oil_temp`| Engine oil temperature, in Celsius
`unit_temp`| Internal temperature of the Cartrack device, in Celsius

Engine coolant and oil temperatures depend on CAN bus support for the vehicle model and return `null` when not available.

## 4) Temperature Alerts​

Temperature-related alerts are configured on the account (through Fleetweb or your Cartrack account manager) and can then be retrieved through the API:

  1. Call [Get Alerts Notification Types](<https://developer.cartrack.com/docs/fleet-api/get-alerts-notification-types>) to list the alert types available on your account. Temperature-related types include `COOLANT_TEMPERATURE`, `ENGINE_TEMPERATURE`, `TEMPERATURE_DIAGNOSTIC`, and the geofence-scoped probe alerts `GEOFENCE_ALERTS_TEMP1_HIGH_WITH_IGNITION_ON` to `GEOFENCE_ALERTS_TEMP4_LOW_WITH_IGNITION_ON` (a high and a low variant per probe).
  2. Call [Get Alerts Notifications](<https://developer.cartrack.com/docs/fleet-api/get-alerts-notifications>) with `filter[alert_type]` set to the relevant type to retrieve the triggered notifications for a period (limited to 31 days per request).

info

Creating temperature alerts through the API is not currently supported. The alert creation endpoints cover other alert types (geofence, ignition, and [PTO and panic sensors](<https://developer.cartrack.com/docs/fleet-api/create-sensor-alert>)); temperature thresholds are configured on the account itself.

## Recommended Integration Pattern​

  1. For **cold chain monitoring** : poll `GET /topics/vehicles/temperature` in latest mode at your refresh interval, and compare `temp1` to `temp4` against your thresholds. Check `event_ts` to make sure the reading is recent before acting on it.
  2. For **temperature history and auditing** : pull `GET /topics/vehicles/temperature` with consecutive 24-hour windows and store the readings on your side.
  3. For **live fleet dashboards** : read the `temp1` to `temp4` fields from `GET /vehicles/status`, which you likely already poll for positions.
  4. For **threshold breach notifications** : configure temperature alerts on the account, then poll `GET /alerts/notifications` filtered by the temperature alert types.
  5. For **engine health monitoring** : use the events endpoints and track `water_temp` and `oil_temp` over time.
