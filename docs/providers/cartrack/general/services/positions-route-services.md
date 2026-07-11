---
source: https://developer.cartrack.com/docs/fleet-api-general/services/positions-route-services
title: Positions and Trip Services
---

# Positions and Trip Services

# Positions and Trip Services

This page explains how to retrieve vehicle positions and trip history with the Fleet API for both business and technical audiences.

## Acronyms and Terms​

  * **API** : Application Programming Interface.
  * **GPS** : Global Positioning System.

## Which Endpoint Should I Use?​

Use this mapping based on the location-related data you need:

  * **All GPS positions / full trip point history** :
    * [Get events for one vehicle](<https://developer.cartrack.com/docs/fleet-api/get-events-for-one-vehicle>)
    * [Get events for all vehicles](<https://developer.cartrack.com/docs/fleet-api/get-events-for-all-vehicles>)
  * **Continuous tracking (near real-time fleet view)** :
    * [Get vehicles status, location, fuel, odometer and more](<https://developer.cartrack.com/docs/fleet-api/get-vehicles-status-location-fuel-odometer-and-more>)
  * **Trip start and end locations** :
    * [Get all trips](<https://developer.cartrack.com/docs/fleet-api/get-all-trips/>)
    * [Get trips by registration](<https://developer.cartrack.com/docs/fleet-api/get-trips-by-registration>)

## 1) All GPS Positions​

### What It Means​

Retrieve historical position events over a period to reconstruct where a vehicle has been.

### Best Endpoints​

  * Single vehicle: [Get events for one vehicle](<https://developer.cartrack.com/docs/fleet-api/get-events-for-one-vehicle>)
  * All vehicles: [Get events for all vehicles](<https://developer.cartrack.com/docs/fleet-api/get-events-for-all-vehicles>)

### Business Interpretation​

  * Use for investigations, trip replay, proof of presence, and operational audits.
  * Best source when users ask for "all points" across a time window.

### Developer Interpretation​

  * Treat events as historical telemetry points.
  * Query by time windows and paginate as needed.
  * For long periods, fetch in chunks to avoid very large responses.

## 2) Continuous Tracking (Near Real-Time)​

### What It Means​

Show the latest known vehicle positions continuously, similar to a live fleet map.

### Best Endpoint​

  * [Get vehicles status, location, fuel, odometer and more](<https://developer.cartrack.com/docs/fleet-api/get-vehicles-status-location-fuel-odometer-and-more>)

### Business Interpretation​

  * Use for day-to-day live operations and dispatcher monitoring.
  * Best for "where is the vehicle now?" use cases.

### Developer Interpretation​

  * Implement polling (for example every 10 to 30 seconds) to refresh latest states.
  * This is near real-time status polling, not a websocket streaming feed.
  * Combine with events endpoints when historical breadcrumb detail is required.

## 3) Full Trip History​

### What It Means​

Depending on your use case, "trip history" can mean either:

  * Full breadcrumb points of a journey, or
  * Trip summaries with start and end locations.

### Endpoint Selection​

  * For **full breadcrumb trip points** : use the events endpoints.
  * For **trip-level start and end locations** :
    * [Get all trips](<https://developer.cartrack.com/docs/fleet-api/get-all-trips/>)
    * [Get trips by registration](<https://developer.cartrack.com/docs/fleet-api/get-trips-by-registration>)

### Business Interpretation​

  * Use trips for reporting and KPI-level trip analysis.
  * Use events when analysts need path-level detail.

### Developer Interpretation​

  * Trips are summary records and should not be treated as complete trip-point datasets.
  * Build full trip replay from events data for the selected time interval.

## Recommended Implementation Pattern​

For most fleet applications:

  1. Poll the vehicle status endpoint for current map state.
  2. Query events endpoints for historical trip playback.
  3. Query trips endpoints for trip reporting (start/end and trip context).
