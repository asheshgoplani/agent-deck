---
source: https://developer.cartrack.com/docs/fleet-api-general/services/vehicle-sensors-services
title: Vehicle Sensors Services
---

# Vehicle Sensors Services

# Vehicle Sensors Services

This page explains the vehicle sensor timeline available in the Fleet API, which sensors are supported, and how to retrieve historical sensor readings.

## Supported Sensors​

The sensor timeline endpoint supports the following sensor types:

Sensor| `filter[sensor]` value| Description
---|---|---
Fuel (CAN bus)| `FUEL`| Fuel level reported via the vehicle's CAN bus
EV Battery| `EV_BATTERY`| State of charge for electric vehicles
EV Charging Status| `EV_BATTERY_CHARGING_STATUS`| Whether the vehicle is currently charging
EV Range| `EV_RANGE`| Estimated remaining range for electric vehicles
EV Consumption| `EV_CONSUMPTION`| Energy consumption for electric vehicles
Taxi| `TAXI`| Taximeter state, available for supported taxi operators

warning

Sensor availability depends on the hardware installed in the vehicle and the sensors configured for the account. If a sensor is not configured, the endpoint returns no data for that vehicle. Contact your Cartrack account manager if you are unsure which sensors are active.

## Which Endpoint Should I Use?​

Use the following endpoint to retrieve sensor timeline data for a single vehicle:

  * Single vehicle: [Get sensor timeline for one vehicle](<https://developer.cartrack.com/docs/fleet-api/get-sensor-timeline-for-one-vehicle>)

## How to Retrieve Sensor Timeline Data​

  1. Identify the vehicle registration and the sensor type you want to query.
  2. Choose a time range. The range between `filter[start_timestamp]` and `filter[end_timestamp]` cannot exceed 31 days.
  3. Call `GET /vehicles/{registration}/sensors/timeline` with `filter[sensor]`, `filter[start_timestamp]`, and `filter[end_timestamp]`.
  4. Use the `value` and `raw_value` fields in each record to interpret the sensor state (see below).
  5. Paginate through results using the `page` and `limit` parameters if the response spans multiple pages.

## Understanding the Response​

Each record in the response contains:

Field| Type| Description
---|---|---
`sensor_id`| integer| Internal sensor type identifier
`sensor_name`| string| Human-readable sensor name
`sensor_no`| integer| Sensor number assigned to the vehicle
`sensor_type`| string| `analog` or `digital`
`event_ts`| string| Timestamp of the sensor reading
`raw_value`| string| The literal value reported by the sensor hardware
`value`| string| A numeric index mapped from `raw_value` (see country-specific notes below)

For most sensors, `raw_value` is the primary field to use. The `value` field is a positional index into a fixed mapping table defined per sensor type and is most relevant for sensors that report named states (such as the Taxi sensor).

## Country-Specific Notes​

### Portugal — Taxi sensor​

Portuguese taxi operators use a taximeter sensor (`sensor_no` 901) connected to the Cartrack device. The taximeter sends state codes as strings; the API maps these to a numeric `value` index for consistency.

`value`| `raw_value`| Meaning
---|---|---
`0`| `DESLIGADO`| Taximeter off / out of service
`1`| `LIVRE`| Free / available for hire
`2`| `1`| Hardware-specific taximeter code
`3`| `3`| Hardware-specific taximeter code
`4`| `5`| Hardware-specific taximeter code
`5`| `6`| Hardware-specific taximeter code
`6`| `C`| Hardware-specific taximeter code
`7`| `P`| Hardware-specific taximeter code
`8`| `-`| Unknown / no data

`DESLIGADO` (off) and `LIVRE` (free) are the standard, well-understood states. The exact meaning of the hardware-specific codes (`1`, `3`, `5`, `6`, `C`, `P`) depends on the taximeter model and should be confirmed with the taximeter vendor.
