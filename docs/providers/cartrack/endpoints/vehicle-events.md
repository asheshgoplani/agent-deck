---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Vehicle Events
spec_version: 1.26.0622.1
---

# Vehicle Events

_4 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [GET `/vehicles/events`](#get-vehicles-events) — Get Events for All Vehicles
- [GET `/vehicles/events/types`](#get-vehicles-events-types) — Get Event Types
- [GET `/vehicles/{registration}/events`](#get-vehicles-registration-events) — Get Events for One Vehicle
- [GET `/vehicles/{registration}/events/idling`](#get-vehicles-registration-events-idling) — Get Idling Events for One Vehicle

## GET `/vehicles/events`

**Get Events for All Vehicles**

Retrieves vehicle events for all vehicles associated with the account within a specified date and time range. The accepted date-time format is "YYYY-MM-DD hh:mm:ss". The time range between `start_timestamp` and `end_timestamp` is limited to a maximum of 24 hours to ensure focused and performant data retrieval. Data is available for approximately the last 5 years. Requests with `start_timestamp` earlier than 5 years ago will be rejected with a 422 error. For more information about vehicle event types and related services, refer to https://developer.cartrack.com/docs/fleet-api-general/services/vehicles-events-services

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `start_timestamp` | **required** | string | Returns data within the specified time range | "2022-01-01 00:00:00" |
| `end_timestamp` | **required** | string | Returns data within the specified time range | "2022-01-01 23:59:59" |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `vehicle-event-base` |  |  |  |
| `meta` | `pagination` |  |  |  |

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

## GET `/vehicles/events/types`

**Get Event Types**

Retrieves a list of all available vehicle event types associated with the account. For more information about vehicle event types, refer to https://developer.cartrack.com/docs/fleet-api-general/services/vehicles-events-services#vehicle-event-types

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

## GET `/vehicles/{registration}/events`

**Get Events for One Vehicle**

This endpoint returns the events for the given dates (YYYY-MM-DD hh:mm:ss) for a given registration. Data is available for approximately the last 5 years. Requests with `start_timestamp` earlier than 5 years ago will be rejected with a 422 error.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `registration` | **required** | string | The vehicle's registration | ABC-12345 |

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `start_timestamp` | **required** | string | Returns data within the specified time range | "2022-01-01 00:00:00" |
| `end_timestamp` | **required** | string | Returns data within the specified time range | "2022-01-01 23:59:59" |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `vehicle-event-base` |  |  |  |
| `meta` | `pagination` |  |  |  |

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

## GET `/vehicles/{registration}/events/idling`

**Get Idling Events for One Vehicle**

Retrieves idling events within specified date and time ranges for a given vehicle registration number. The format for dates is YYYY-MM-DD hh:mm:ss. Data is available for approximately the last 5 years. Requests with `start_timestamp` earlier than 5 years ago will be rejected with a 422 error.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `registration` | **required** | `registration` | The registration number of the vehicle for which idling events are requested. | ABC1234 |

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `start_timestamp` | **required** | string | Returns data within the specified time range | "2022-01-01 00:00:00" |
| `end_timestamp` | **required** | string | Returns data within the specified time range | "2022-01-01 23:59:59" |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object | **required** |  |  |
| `meta` | `pagination` | **required** |  |  |

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

