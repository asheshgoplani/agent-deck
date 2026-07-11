---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Vehicle (Electric)
spec_version: 1.26.0622.1
---

# Vehicle (Electric)

_8 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [GET `/vehicles/{registration}/soc`](#get-vehicles-registration-soc) — Get Electric Vehicle State of Charge (SoC) Events by Registration
- [GET `/vehicles/soc/latest`](#get-vehicles-soc-latest) — Get Electric Vehicles State of Charge (SoC)
- [GET `/vehicles/charging/latest`](#get-vehicles-charging-latest) — Get Electric Vehicles Charging State
- [GET `/vehicles/{registration}/charging/events`](#get-vehicles-registration-charging-events) — Get Electric Vehicle Charging Events by Registration
- [GET `/vehicles/{registration}/range`](#get-vehicles-registration-range) — Get Electric Vehicle Remaining Range Events by Registration
- [POST `/vehicles/ev-consumption`](#post-vehicles-ev-consumption) — Retrieve Electric Vehicles Estimated Battery Consumptions
- [POST `/vehicles/range`](#post-vehicles-range) — Retrieve Multiple Electric Vehicles Remaining Range Events
- [POST `/vehicles/soc`](#post-vehicles-soc) — Retrieve Multiple Electric Vehicles State of Charge (SoC) Events

## GET `/vehicles/{registration}/soc`

**Get Electric Vehicle State of Charge (SoC) Events by Registration**

This endpoint returns the events for the given dates (YYYY-MM-DD hh:mm:ss) for a given registration

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `registration` | **required** | `registration` | The vehicle's license plate | ABC1234 |

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
| `data` | array of object |  |  |  |
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

## GET `/vehicles/soc/latest`

**Get Electric Vehicles State of Charge (SoC)**

This endpoint returns the latest State of Charge (SoC) for all electric vehicles in the fleet.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `page` | optional | integer | The current page | 1 |
| `limit` | optional | integer | The number of items to display per page | 15 |

### Request Body

_No request body._

### Responses

#### `200` — successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  |  |  |
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

## GET `/vehicles/charging/latest`

**Get Electric Vehicles Charging State**

This endpoint returns the latest charging state for all electric vehicles in the fleet.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `page` | optional | integer | The current page | 1 |
| `limit` | optional | integer | The number of items to display per page | 15 |

### Request Body

_No request body._

### Responses

#### `200` — successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  |  |  |
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

## GET `/vehicles/{registration}/charging/events`

**Get Electric Vehicle Charging Events by Registration**

This endpoint returns the charging events for a given electric vehicle over a given period.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `registration` | **required** | `registration` | The vehicle's license plate | ABC1234 |

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `start_timestamp` | **required** | string | Returns data within the specified time range | "2022-01-01 00:00:00" |
| `end_timestamp` | **required** | string | Returns data within the specified time range | "2022-01-01 23:59:59" |
| `page` | optional | integer | The current page | 1 |
| `limit` | optional | integer | The number of items to display per page | 15 |

### Request Body

_No request body._

### Responses

#### `200` — successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  | The list of events generated during that period |  |
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

## GET `/vehicles/{registration}/range`

**Get Electric Vehicle Remaining Range Events by Registration**

This endpoint provides the EV range reported events for a specific registration, covering the given dates (YYYY-MM-DD hh:mm:ss)

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `registration` | **required** | `registration` | The vehicle's registration | ABC1234 |

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
| `data` | array of object |  |  |  |
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

## POST `/vehicles/ev-consumption`

**Retrieve Electric Vehicles Estimated Battery Consumptions**

This endpoint allows you to retrieve the estimated electric vehicle battery consumptions for up to 100 vehicles per request within your fleet, at the start and at the end of a given period with a maximum time range of 24 hours. The values are estimated based on energy consumption and distance traveled.  
  
 \*\*Note\*\*: This endpoint is subject to a rate limit, allowing a maximum of 10 requests per minute.

### Request Body

The json data that needs to be processed

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `vehicle_id` | `vehicle-id` |  |  |  |
| `registrations` | array of `registration` | **required** | Array of vehicle registrations | ["ABC1234X", "DEF5678X"] |
| `start_timestamp` | `date` | **required** | This will return the battery consumption between the start and end timestamp | "2022-01-01 00:00:00" |
| `end_timestamp` | `date` | **required** | This will return the fuel level between the start and end timestamp.       The lookup period is of \*\*maximum 24 hours\*\* between the start\_timestamp and the end\_timestamp | "2022-01-01 23:59:59" |
| `page` | integer |  | The current page | 1 |
| `limit` | integer |  | The number of items to display per page | 10 |

### Responses

#### `200` — successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  |  |  |
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

## POST `/vehicles/range`

**Retrieve Multiple Electric Vehicles Remaining Range Events**

This endpoint allows you to retrieve the EV range reported events for up to 100 vehicles per request within your fleet, at the start and at the end of a given period with a maximum time range of 24 hours.  
  
 \*\*Note\*\*: This endpoint is subject to a rate limit, allowing a maximum of 10 request per minute.

### Request Body

The json data that needs to be processed

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `vehicle_id` | `vehicle-id` |  |  |  |
| `registrations` | array of `registration` | **required** | Array of vehicle registrations | ["ABC1234X", "DEF5678X"] |
| `start_timestamp` | `date` | **required** | This will return the remaining range between the start and end timestamp | "2022-01-01 00:00:00" |
| `end_timestamp` | `date` | **required** | This will return the remaining range between the start and end timestamp.       The lookup period is of \*\*maximum 24 hours\*\* between the start\_timestamp and the end\_timestamp | "2022-01-01 23:59:59" |
| `page` | integer |  | The current page | 1 |
| `limit` | integer |  | The number of items to display per page | 10 |

### Responses

#### `200` — successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  |  |  |
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

## POST `/vehicles/soc`

**Retrieve Multiple Electric Vehicles State of Charge (SoC) Events**

This endpoint allows you to retrieve the EV state of charge (SoC) reported events for up to 100 vehicles per request within your fleet, at the start and at the end of a given period with a maximum time range of 24 hours.  
  
 \*\*Note\*\*: This endpoint is subject to a rate limit, allowing a maximum of 10 request per minute.

### Request Body

The json data that needs to be processed

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `registrations` | array of string | **required** | Array of vehicle registrations | ["ABC1234X", "DEF5678X"] |
| `start_timestamp` | `date` | **required** | The earliest date and time to retrieve events (limited to **24 hours**) | "2022-01-01 00:00:00" |
| `end_timestamp` | `date` | **required** | The latest date and time to retrieve events.       The lookup period is of \*\*maximum 24 hours\*\* between the start\_timestamp and the end\_timestamp | "2022-01-01 23:59:59" |
| `page` | integer |  | The current page | 1 |
| `limit` | integer |  | The number of items to display per page | 10 |

### Responses

#### `200` — successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  |  |  |
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

