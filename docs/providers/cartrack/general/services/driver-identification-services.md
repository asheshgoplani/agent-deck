---
source: https://developer.cartrack.com/docs/fleet-api-general/services/driver-identification-services
title: Driver Identification Services
---

# Driver Identification Services

# Driver Identification Services

This page explains how driver assignment works in the Fleet API, including driver tags, default drivers, mobile app assignment, and API linkage.

## Acronyms and Terms​

  * **API** : Application Programming Interface.
  * **RFID** : Radio-Frequency Identification.

## Which Endpoint Should I Use?​

Use this mapping based on how you want to identify drivers for vehicles:

  * **Current driver snapshot across the fleet** :
    * [Get Vehicle's Status: location, fuel, odometer and more](<https://developer.cartrack.com/docs/fleet-api/get-vehicles-status-location-fuel-odometer-and-more>)
  * **Set or change a vehicle 's default (fallback) driver**:
    * [Update a Vehicle's Details](<https://developer.cartrack.com/docs/fleet-api/update-a-vehicles-details>)
  * **Audit RFID driver tag reads over time** :
    * [Get Driver Tags Events](<https://developer.cartrack.com/docs/fleet-api/get-driver-tags-events>)
  * **Assign driver from a third-party integration (RFID-like behavior)** :
    * [Link Driver to a Vehicle](<https://developer.cartrack.com/docs/fleet-api/link-driver-to-a-vehicle>)
  * **Clear or inspect API-created linkages** :
    * [Unlink Driver from a Vehicle](<https://developer.cartrack.com/docs/fleet-api/unlink-driver-from-a-vehicle>)
    * [Retrieve Current Linkages](<https://developer.cartrack.com/docs/fleet-api/retrieve-current-linkages>)

## Driver Association Methods​

Drivers can associate to vehicles in multiple ways:

  1. **Default driver on the vehicle** :
     * Persistent/fallback assignment.
     * Used when no temporary assignment is currently detected.
  2. **RFID driver tag read in vehicle** (requires compatible fitment):
     * Driver tags in while ignition is ON.
     * Association is kept until ignition is OFF, or another driver tag is read and overrides it.
  3. **Cartrack driver mobile app assignment** :
     * Driver assigns themselves in the app.
  4. **Vehicle Driver Linkage API** :
     * Third-party system links a driver to a vehicle.
     * Useful when reproducing RFID-style behavior through integration.

## How Driver Tags Help Identify the Driver​

Driver tags provide an operational way to identify who is currently driving, without relying only on static/default vehicle ownership.

  * They capture a per-use driver context while ignition is ON.
  * They can override a previously assigned temporary driver during the same ignition cycle.
  * Their events can be audited historically through the driver tags events endpoint.

This helps teams improve trip accountability and driver-level reporting for shared vehicles.

## Driver Resolution in Vehicle Status​

`/vehicles/status` is a snapshot service and returns the current driver context for each vehicle.

  * If an active temporary association exists (RFID tag flow or linkage API flow), that driver is shown as the current driver.
  * If no temporary association is active, the vehicle default driver is used as the fallback value.
  * RFID association is cleared at ignition OFF.

## Recommended Integration Pattern​

For most integrations:

  1. Set and maintain default drivers as baseline ownership.
  2. Use one temporary assignment flow for active-driver identification.
  3. Poll vehicle status for operational snapshots.
  4. Query driver tag events for historical audit and investigations.

Integration Constraint

Vehicle Driver Linkage endpoints should not be used together with other Cartrack driver-vehicle assignment services (for example, driver mobile app or driver tags) in the same operational flow.
