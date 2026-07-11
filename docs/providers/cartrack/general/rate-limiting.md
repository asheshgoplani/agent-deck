---
source: https://developer.cartrack.com/docs/fleet-api-general/rate-limiting
title: Rate Limiting
---

# Rate Limiting

# Rate Limiting

The Cartrack Fleet API applies rate limiting to all incoming API requests to protect network resources, maintain system performance, and ensure a consistent experience for all users.

### Default Rate Limit​

The API has a default rate limit of **1,000 requests per minute**. This limit applies across most API endpoints unless otherwise specified. Any requests exceeding this limit will receive an HTTP `429 Too Many Requests` response.

### API Endpoint Specific Rate Limit​

Some API endpoints have stricter rate limits to ensure fairness and prevent abuse. Below are the specific limits for these endpoints:

**Endpoint**| **Rate Limit**| **Description**
---|---|---
[POST /fuel/consumed](</docs/fleet-api/retrieve-fuel-consumed-sensor-data-for-multiple-vehicles>)| 10 requests per minute| Retrieve fuel used estimate for multiple vehicles.
[POST /fuel/level](</docs/fleet-api/retrieve-fuel-used-estimate-for-multiple-vehicles>)| 10 requests per minute| Retrieve fuel consumed sensor data for multiple vehicles.
[POST /vehicles/ev-consumption](</docs/fleet-api/retrieve-electric-vehicles-estimated-battery-consumptions>)| 10 requests per minute| Retrieve battery consumption for multiple electric vehicles.
[POST /vehicles/range](</docs/fleet-api/retrieve-multiple-electric-vehicles-remaining-range>)| 10 requests per minute| Retrieve EV range reported events for multiple electric vehicles.
[POST /vehicles/soc](</docs/fleet-api/retrieve-multiple-electric-vehicles-state-of-charge-so-c-events>)| 10 requests per minute| Retrieve state of charge (SoC) events for multiple electric vehicles.
[GET /vehicles/status](</docs/fleet-api/get-vehicles-status-location-fuel-odometer-and-more>)| 60 requests per minute| Retrieve the latest snapshot for the entire fleet.
[GET /vehicles/events](</docs/fleet-api/get-events-for-all-vehicles>)| 60 requests per minute| Retrieve events for all vehicles in the fleet.
[GET /vehicles/{registration}/events](</docs/fleet-api/get-events-for-one-vehicle>)| 200 requests per minute| Retrieve events for a specific vehicle.
[GET /vehicles/{registration}/events/idling](</docs/fleet-api/get-idling-events-for-one-vehicle>)| 200 requests per minute| Retrieve idling events for a specific vehicle.
[GET /vehicles/vext](</docs/fleet-api/get-vehicles-vext-at-ignition-off>)| 10 requests per minute| Retrieve vehicles VEXT data regardless of ignition status.

For endpoint-specific rate limits, exceeding the limit will also result in an HTTP `429 Too Many Requests` response.

### Vehicle Command Cooldown​

In addition to the request rate limits above, the vehicle command endpoints apply a cooldown to prevent duplicate commands:

  * [PUT /vehicles/{registration}/central-locking](</docs/fleet-api/send-command-to-lock-or-unlock-a-vehicle>)
  * [POST /vehicles/commands/{registration}](</docs/fleet-api/send-command-to-sound-horn-or-turn-on-hazard-lights-on-vehicle>)

Once a command is accepted, the same command cannot be sent again to the same vehicle while the previous one is still being processed, for up to **30 seconds**. During this window the API responds with an HTTP `409 Conflict` and a `Retry-After` header indicating the number of seconds to wait before retrying.

The cooldown applies per vehicle and per command: sending a different command to the same vehicle, or the same command to a different vehicle, is not affected.

**Example Response**


    HTTP/1.1 409 Conflict


    Content-Type: application/json


    Retry-After: 30





    {


        "data": null,


        "error": {


            "code": 409,


            "message": "Cannot send lock/lock simultaneously. Try again later."


        }


    }


### Retry Behavior​

If you receive a HTTP `429 Too Many Requests` response, you must respect the headers included in the response. These headers provide information about when you can retry:

  * `X-RateLimit-Retry-At`: The timestamp of the next earliest retry.
  * `X-RateLimit-Retry-After-Seconds`: The number of seconds until the next earliest retry.

**Example Response**


    HTTP/1.1 429 Too Many Requests


    Content-Type: application/json


    X-RateLimit-Retry-At: 1737592000


    X-RateLimit-Retry-After-Seconds: 15





    {


        "error": {


            "code": 429,


            "message": "Too many requests. Please wait before retrying."


        }


    }


**How to Handle`429 Too Many Requests`**

  * **Wait and Retry** : Respect the `X-RateLimit-Retry-At` and `X-RateLimit-Retry-After-Seconds` header to wait for the recommended time before retrying.
  * **Exponential Backoff** : Implement exponential backoff with jitter to avoid repeated rate-limit violations.

**Notes** : Repeated violations of rate limits may result in temporary or permanent access restrictions.

### Increasing the Limit​

If your application requires a higher rate limit, you may submit a request for a limit increase. Requests will be evaluated on a case-by-case basis to ensure compatibility with our infrastructure. Approved limits will be customized and communicated accordingly.
