---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Vehicle Status
spec_version: 1.26.0622.1
---

# Vehicle Status

_1 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [GET `/vehicles/status`](#get-vehicles-status) — Get Vehicle's Status: location, fuel, odometer and more

## GET `/vehicles/status`

**Get Vehicle's Status: location, fuel, odometer and more**

Get a glance at your vehicles's latest status, including fuel, location, driver and other telematic data. This endpoint is limited to a maximum of 60 calls per minute, see https://developer.cartrack.com/docs/fleet-api-general/rate-limiting for more information.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `filter[engine_type]` | optional | string | Filter by vehicle engine type (e.g. combustion, electric, etc.), case insensitive, can be partial match. | Electric |
| `filter[vehicle_id]` | optional | integer | Optional filter to retrieve by vehicle ID. | 12345 |
| `filter[chassis_number]` | optional | `chassis-number` | Filter by chassis number, case insensitive, can be partial match |  |
| `filter[ignition]` | optional | string | Filters vehicles by ignition status. true for ignition on, false for ignition off. | true |
| `odometer_in_km` | optional | string | If true, the odometer will be returned in kilometers, otherwise in meters. | true |

### Request Body

_No request body._

### Responses

#### `200` — successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  |  |  |

#### `400` — Validation failed for the input parameters.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

#### `401` — Unauthorized access. Authentication is required.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

#### `403` — Access is forbidden. The user does not have permission to access this resource.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

#### `404` — The requested resource was not found.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

#### `500` — Internal server error.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |


---

