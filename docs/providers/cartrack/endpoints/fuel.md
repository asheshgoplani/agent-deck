---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Fuel
spec_version: 1.26.0622.1
---

# Fuel

_7 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [GET `/fuel/consumed/{registration}`](#get-fuel-consumed-registration) — Get a vehicle's fuel consumed sensor data
- [POST `/fuel/consumed`](#post-fuel-consumed) — Retrieve fuel consumed sensor data for multiple vehicles
- [GET `/fuel/fills/{registration}`](#get-fuel-fills-registration) — Get fuel fills for a vehicle
- [GET `/fuel/fills`](#get-fuel-fills) — Get fuel fills for all vehicles
- [GET `/fuel/level/{registration}`](#get-fuel-level-registration) — Get fuel used estimate for a vehicle
- [POST `/fuel/level`](#post-fuel-level) — Retrieve fuel used estimate for multiple vehicles
- [GET `/fuel/level/history/{registration}`](#get-fuel-level-history-registration) — Get fuel level history for a vehicle

## GET `/fuel/consumed/{registration}`

**Get a vehicle's fuel consumed sensor data**

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `registration` | **required** | `registration` | The vehicle's license plate | ABC1234 |

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `start_timestamp` | **required** | `date` | This will return the fuel consumption between the start and end timestamp | 2024-06-01 00:00:00 |
| `end_timestamp` | **required** | `date` | This will return the fuel consumption between the start and end timestamp.       The lookup period is of **maximum 31 days** between the start\_timestamp and the end\_timestamp | 2024-06-30 23:59:59 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  |  |  |

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

#### `422` — Validation failed for the input parameters.

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

## POST `/fuel/consumed`

**Retrieve fuel consumed sensor data for multiple vehicles**

This endpoint allows you to retrieve vehicle fuel consumption for a give period. The vehicles must have the fuel consumed sensor (sensor type id 20) and Cartrack telematic device must be connected to the CAN bus. It returns the value in litres (coefficient applied). Reported events for up to 100 vehicles per request within your fleet, at the start and at the end of a given period with a maximum time range of 31 days.  
  
 **Note**: This endpoint is subject to a rate limit, allowing a maximum of 10 request per minute.

### Request Body

The json data that needs to be processed

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `registrations` | array of `registrations` | **required** | Array of vehicle registrations | ["ABC1234X", "DEF5678X"] |
| `start_timestamp` | `date` | **required** | This will return the remaining range between the start and end timestamp |  |
| `end_timestamp` | `date` | **required** | This will return the remaining range between the start and end timestamp.       The lookup period is of **maximum 24 hours** between the start\_timestamp and the end\_timestamp |  |
| `page` | `page` |  |  |  |
| `limit` | `limit` |  |  |  |

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  |  |  |
| `meta` | `pagination` |  |  |  |

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

#### `422` — Validation failed for the input parameters.

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

## GET `/fuel/fills/{registration}`

**Get fuel fills for a vehicle**

This endpoint retrieves a vehicles's fuel fills for a specified period, with a maximum duration of 31 days. The API requires the tracker to be configured to read the fuel sensor data. It returns the fuel fill values in liters (with coefficient applied).

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `registration` | **required** | `registration` | The vehicle's license plate | ABC1234 |

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `start_timestamp` | **required** | `date` | This will return the fuel consumption between the start and end timestamp | 2024-06-01 00:00:00 |
| `end_timestamp` | **required** | `date` | This will return the fuel consumption between the start and end timestamp.       The lookup period is of **maximum 31 days** between the start\_timestamp and the end\_timestamp | 2024-06-30 23:59:59 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  | This array returns the list of fuel fills |  |
| `meta` | `pagination` |  |  |  |

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

#### `422` — Validation failed for the input parameters.

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

## GET `/fuel/fills`

**Get fuel fills for all vehicles**

This endpoint retrieves fuel fills for all vehicles within your fleet, with a maximum time range of 24 hours. The API requires the vehicles to have the fuel sensor data accessible by the tracker. It returns the fuel fill values in liters (with coefficient applied).

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `start_timestamp` | **required** | `date` | This will return the fuel consumption between the start and end timestamp | 2024-06-01 00:00:00 |
| `end_timestamp` | **required** | `date` | This will return the fuel consumption between the start and end timestamp.       The lookup period is of **maximum 24 hours** between the start\_timestamp and the end\_timestamp | 2024-06-01 00:00:00 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  | This array returns the list of fuel fills |  |
| `meta` | `pagination` |  |  |  |

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

#### `422` — Validation failed for the input parameters.

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

## GET `/fuel/level/{registration}`

**Get fuel used estimate for a vehicle**

This endpoint allows you to retrieve the fuel level for a specific vehicle, at the start and at the end of a given period. To use this API, your vehicle must be configured to read the fuel sensor data. The fuel level data is returned in liters with a coefficient applied. Our algorithm also give you the estimated fuel used over the period.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `registration` | **required** | `registration` | The vehicle's license plate | ABC1234 |

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `start_timestamp` | **required** | `date` | This will return the fuel level between the start and end timestamp | 2024-06-01 00:00:00 |
| `end_timestamp` | **required** | `date` | This will return the fuel level between the start and end timestamp.       The lookup period is of **maximum 31 days** between the start\_timestamp and the end\_timestamp | 2024-06-30 23:59:59 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  |  |  |

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

#### `422` — Validation failed for the input parameters.

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

## POST `/fuel/level`

**Retrieve fuel used estimate for multiple vehicles**

This endpoint allows you to retrieve the fuel level for up to 100 vehicles per request within your fleet, at the start and at the end of a given period with a maximum time range of 24 hours. To use this API, your vehicle must be configured to read the fuel sensor data. The fuel level data is returned in liters with a coefficient applied. Our algorithm also give you the estimated fuel used over the period.  
  
 **Note**: This endpoint is subject to a rate limit, allowing a maximum of 10 request per minute.

### Request Body

The json data that needs to be processed

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `registrations` | array of `registrations` | **required** | Array of vehicle registrations | ["ABC1234X", "DEF5678X"] |
| `start_timestamp` | `date` | **required** | This will return the fuel level between the start and end timestamp |  |
| `end_timestamp` | `date` | **required** | This will return the fuel level between the start and end timestamp.       The lookup period is of **maximum 24 hours** between the start\_timestamp and the end\_timestamp |  |
| `page` | `page` |  |  |  |
| `limit` | `limit` |  |  |  |

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  |  |  |
| `meta` | `pagination` |  |  |  |

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

#### `422` — Validation failed for the input parameters.

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

## GET `/fuel/level/history/{registration}`

**Get fuel level history for a vehicle**

This endpoint allows you to retrieve the fuel level history for a specific vehicle within a specified time frame. To use this API, your vehicle must be configured to real the fuel sensor data. The fuel level data is returned in liters with a coefficient applied. **We strongly discourage you to use this API to estimate the fuel use.**

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `registration` | **required** | `registration` | The vehicle's license plate | ABC1234 |

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `start_timestamp` | **required** | `date` | This will return the fuel level between the start and end timestamp | 2024-06-01 00:00:00 |
| `end_timestamp` | **required** | `date` | This will return the fuel level between the start and end timestamp.       The lookup period is of **maximum 31 days** between the start\_timestamp and the end\_timestamp | 2024-06-30 23:59:59 |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  |  |  |
| `meta` |  |  |  |  |

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

#### `422` — Validation failed for the input parameters.

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

