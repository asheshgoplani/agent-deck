---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Topics
spec_version: 1.26.0622.1
---

# Topics

_2 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [GET `/topics/vehicles/door`](#get-topics-vehicles-door) — Get Vehicles Door Open/Close Events
- [GET `/topics/vehicles/temperature`](#get-topics-vehicles-temperature) — Get Vehicles Temperature Data

## GET `/topics/vehicles/door`

**Get Vehicles Door Open/Close Events**

This endpoint allows you to retrieve door open/close event data for all vehicles in the account.   
  
 **Note:** This endpoint supports Topic-Based Access Control. Grant the `DOOR` topic to a subuser to provide the same data access as the account administrator for this endpoint.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `start_timestamp` | **required** | `date` | Start date and time to retrieve events from. | 2022-01-01 00:00:00 |
| `end_timestamp` | **required** | `date` | End date and time to retrieve events up to. The lookup period has a **maximum of 31 days**. | 2022-01-01 23:59:59 |
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

## GET `/topics/vehicles/temperature`

**Get Vehicles Temperature Data**

This endpoint allows you to retrieve vehicle temperature data from all vehicles in the account.   
  
 **Note:** This endpoint supports Topic-Based Access Control. Grant the `TEMPERATURE` topic to a subuser to provide the same data access as the account administrator for this endpoint.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `start_timestamp` | **required** | `date` | Start date and time to retrieve events from. | 2022-01-01 00:00:00 |
| `end_timestamp` | **required** | `date` | End date and time to retrieve events up to. The lookup period has a **maximum of 31 days**. | 2022-01-01 23:59:59 |
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

