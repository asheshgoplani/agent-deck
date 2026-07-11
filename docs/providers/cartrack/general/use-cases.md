---
source: https://developer.cartrack.com/docs/fleet-api-general/use-cases
title: Use Cases
---

# Use Cases

# Use Cases

The Cartrack Fleet API offers a diverse range of services designed to cater to various operational needs across your fleet management spectrum. Below are a few use cases for our API, segmented by themes to help you understand how you can integrate these into your systems for better fleet oversight and enhanced operational efficiency:

## Vehicle Management, Fuel Monitoring, Trip Reporting, and Events​

  * **Vehicles API** : Gain real-time data on your fleet, including vehicle status updates, locations, and operational metrics. For instance, use the `/vehicles/status` endpoint for a comprehensive overview of all your vehicles, helping you monitor their condition and availability.
  * **Trips API** : Access detailed trip data such as route information, driving behaviors, and idle times. This API is invaluable for tracking driver performance and vehicle usage, essential for optimizing routes and improving fuel efficiency.
  * **Fuel API** : Monitor fuel levels and usage across your fleet. Whether you are gathering data directly from the CAN bus or using analog sensors, the fuel API helps you track fuel consumption accurately, manage costs, and prevent fuel fraud. See [Fuel Services](</docs/fleet-api-general/services/fuel-services>) for detailed concepts and endpoint mapping.
  * **Driver Identification Services** : Understand how drivers are associated to vehicles using default assignment, RFID driver tags, mobile app assignment, and linkage APIs. See [Driver Identification Services](</docs/fleet-api-general/services/driver-identification-services>) for driver-resolution behavior and endpoint mapping.
  * **Positions and Trip Services** : Retrieve live location state, historical GPS positions, and trip start/end location information. See [Positions and Trip Services](</docs/fleet-api-general/services/positions-route-services>) for endpoint mapping by use case.
  * **Vehicle Events Services** : Understand event types emitted by vehicles and how to retrieve them from events endpoints. See [Vehicle Events Services](</docs/fleet-api-general/services/vehicles-events-services>) for the complete event-type table and integration guidance.
  * **Vehicle Sensors Services** : Retrieve the historical timeline of sensor readings for a vehicle, filtered by sensor type and time range. Useful for auditing, reporting, and dashboards. See [Vehicle Sensors Services](</docs/fleet-api-general/services/vehicle-sensors-services>) for supported sensors and integration guidance.
  * **Mileage and Odometer Services** : Track total distance travelled per vehicle over any period, retrieve per-trip distance breakdowns, and read current odometer values. Understand the difference between CAN bus and GPS-derived odometer data and how to handle odometer resets. See [Mileage and Odometer Services](</docs/fleet-api-general/services/mileage-odometer-services>) for endpoint mapping and integration guidance.
  * **Vehicle Temperature Services** : Monitor cargo temperature probes and engine temperatures across your fleet, retrieve latest or historical readings, and receive temperature alert notifications. Ideal for refrigerated transport and cold chain compliance. See [Vehicle Temperature Services](</docs/fleet-api-general/services/vehicle-temperature-services>) for endpoint mapping and integration guidance.

## Remote Vehicle Interaction​

  * **Commands API** : Send commands directly to vehicles for functions like locking or unlocking doors, an essential feature for enhancing vehicle security and operational flexibility. The type of commands available can vary based on the hardware installed in your vehicles.

## Geofencing for Asset Management​

  * **Geofences API** : Define virtual perimeters for real-time monitoring of your assets. Track your vehicles as they enter or leave designated areas, helping you manage asset utilization, secure vehicles, and optimize operational zones. (`/geofences[...]`)

## Delivery Management​

  * **Delivery Job Services** : Create delivery jobs from ERP or dispatch systems with clear confirmation patterns for both batch and single-job flows. See [Delivery Job Services](</docs/fleet-api-general/services/delivery-job-services>) for endpoint mapping and failure-handling guidance.

## Video and Camera Management​

  * **Vision Services** : Request recorded video clips from Cartrack Vision cameras, track clip processing status, retrieve livestream links, and upload footage from custom third-party camera devices. Note that Vision API access requires the **Cartrack Vision API** option to be enabled on the account — contact your Cartrack customer success manager if you receive HTTP 403. See [Vision Services](</docs/fleet-api-general/services/vision-services>) for endpoint mapping and integration guidance.

## Fleet Cost Management with MiFleet​

  * **MiFleet Services API** : Utilize advanced analytics and business intelligence to reduce operational costs and enhance decision-making. Monitor and manage expenses related to accidents, insurance, maintenance, and more. Note that MiFleet is an add-on service requiring an additional subscription. (`/mifleet[...]`)

These use cases are just the tip of the iceberg. Each API is designed to provide specific functionalities that can be integrated into your existing systems, providing you with the tools you need to manage your fleet more effectively. Whether you're a developer tasked with enhancing fleet system integrations or a business manager looking to streamline operations, Cartrack’s Fleet API is your gateway to a smarter fleet management solution.
