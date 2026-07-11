---
source: https://developer.cartrack.com/docs/fleet-api-general/services/fuel-services
title: Fuel Services
---

# Fuel Services

# Fuel Services

This page explains the fuel-related services in the Fleet API for both business and technical audiences.

## Acronyms and Terms​

  * **API** : Application Programming Interface.
  * **ECU** : Engine Control Unit. The in-vehicle computer that controls and monitors engine operation.
  * **CAN bus** : Controller Area Network bus. The vehicle network used by electronic modules to exchange data.

## Fuel Concepts at a Glance​

  * **Fuel consumed** : A cumulative value from the ECU that represents total fuel burned by the engine since a reference point.
  * **Fuel level** : Fuel quantity in the tank, read from an analog sensor or from the CAN bus.
  * **Fuel fills and estimated fuel used** : Events and usage periods inferred from fuel level trends over time.

## How to Check Sensor Availability Per Vehicle​

Before calling fuel endpoints, you can verify whether each vehicle has the required sensors by using:

  * [Get vehicles list](<https://developer.cartrack.com/docs/fleet-api/get-vehicles-list>)

In the response, check `data.sensors.*`:

  * `data.sensors.fuel_canbus_consumed`: `true` when CAN bus fuel consumed data is available.
  * `data.sensors.fuel_canbus_level`: `true` when CAN bus fuel level data is available.
  * `data.sensors.fuel_analog_level`: `true` when an analog fuel level sensor is installed.
  * `data.sensors.electric_battery`: `true` when an electric battery sensor is installed.
  * `data.sensors.electric_charging`: `true` when an electric charging status sensor is installed.

### Fuel Service Mapping​

  * **Fuel consumed services** require `data.sensors.fuel_canbus_consumed = true`.
  * **Fuel level services** require at least one of:
    * `data.sensors.fuel_canbus_level = true`, or
    * `data.sensors.fuel_analog_level = true`.
  * **Fuel fills / estimated fuel used** also depend on fuel level availability, so they require at least one of the two fuel level flags above.

### Example​


    {


      "data": {


        "vehicle_id": "123456",


        "sensors": {


          "fuel_canbus_consumed": true,


          "fuel_canbus_level": true,


          "fuel_analog_level": false,


          "electric_battery": false,


          "electric_charging": false


        }


      }


    }


## 1) Fuel Consumed (ECU cumulative value)​

### Definition​

`fuel_consumed` is the total volume of fuel consumed by the vehicle engine since a defined reference point (typically vehicle commissioning or factory release).
It is a monotonically increasing cumulative value reported by the vehicle ECU.

### What It Is Not​

  * Fuel currently in the tank.
  * Fuel used during one trip only.
  * Fuel used since ignition turned on.

### What It Is​

  * Total engine fuel burned over the vehicle lifetime (from the ECU reference point).
  * Computed internally by the ECU from injection timing and flow models.
  * Independent of refueling events.

### Business Interpretation​

  * Useful for long-term fuel performance and cost analysis.
  * Best suited for lifetime or long-period efficiency tracking.
  * Should not be used as a direct substitute for current tank fuel.

### Developer Interpretation​

  * Treat as a cumulative counter.
  * Compute period usage by subtracting two valid readings.
  * Expect no value when the required capability is not enabled on the vehicle.

### Endpoints​

  * Single vehicle: [Get a vehicle's fuel consumed sensor data](<https://developer.cartrack.com/docs/fleet-api/get-a-vehicles-fuel-consumed-sensor-data>)
  * Multiple vehicles: [Retrieve fuel consumed sensor data for multiple vehicles](<https://developer.cartrack.com/docs/fleet-api/retrieve-fuel-consumed-sensor-data-for-multiple-vehicles>)

### Availability​

Fuel consumed data requires the Cartrack fuel-consumed capability to be available for the vehicle.
If the API returns no result, contact your Cartrack sales representative to confirm availability.

## 2) Fuel Level (analog sensor or CAN bus)​

### Definition​

Fuel level is the measured amount of fuel in the tank at a point in time, read either from:

  * An analog fuel sensor.
  * The CAN bus.

### Business Interpretation​

  * Useful for operational monitoring, fuel theft detection, and refill oversight.
  * Provides tank-state visibility rather than lifetime engine burn.

### Developer Interpretation​

  * Treat as time-series telemetry.
  * Expect variation due to movement, slosh, sensor characteristics, and sampling.

### Endpoint​

  * Vehicle history: [Get fuel level history for a vehicle](<https://developer.cartrack.com/docs/fleet-api/get-fuel-level-history-for-a-vehicle>)

### Availability​

Fuel level services require available fuel level readings for the vehicle.
If no data is returned, contact your Cartrack sales account manager to confirm availability.

## 3) Fuel Fills and Estimated Fuel Used (derived from fuel level)​

### Definition​

Cartrack identifies:

  * **Fuel fill periods** from increases in fuel level.
  * **Estimated fuel used periods** from decreases in fuel level.

Both outputs are estimated from fuel level readings and require fuel level data availability.

### Business Interpretation​

  * Helps reconcile refueling activity and consumption patterns.
  * Supports exception analysis (unexpected fills, unusual usage).

### Developer Interpretation​

  * Treat these as derived analytics, not direct ECU counters.
  * Data quality depends on fuel level signal quality and coverage.

### Endpoints​

  * Fuel fills (single vehicle): [Get fuel fills for a vehicle](<https://developer.cartrack.com/docs/fleet-api/get-fuel-fills-for-a-vehicle>)
  * Fuel fills (all vehicles): [Get fuel fills for all vehicles](<https://developer.cartrack.com/docs/fleet-api/get-fuel-fills-for-all-vehicles>)
  * Estimated fuel used (single vehicle): [Get fuel used estimate for a vehicle](<https://developer.cartrack.com/docs/fleet-api/get-fuel-used-estimate-for-a-vehicle>)

### Availability​

Fuel fills and estimated fuel used rely on fuel level readings.
If no data is returned, confirm fuel level availability for the vehicle with your Cartrack sales account manager.
