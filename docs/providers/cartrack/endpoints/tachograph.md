---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Tachograph
spec_version: 1.26.0622.1
---

# Tachograph

_3 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [GET `/tachographs`](#get-tachographs) — Get All Tachograph Files
- [GET `/tachographs/download`](#get-tachographs-download) — Download Tachograph File
- [GET `/tachographs/driving-times`](#get-tachographs-driving-times) — Retrieve Tachograph Driver Driving Times

## GET `/tachographs`

**Get All Tachograph Files**

This endpoint returns all/filtered list of tachograph files.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[created_at_from]` | optional | `date` | This will return files from this date and time. | "2022-01-01 00:00:00" |
| `filter[created_at_to]` | optional | `date` | This will return files to this date and time. | "2022-01-01 23:59:59" |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  | List of files. |  |

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

## GET `/tachographs/download`

**Download Tachograph File**

This endpoint allows you to download a tachograph file based on the filename.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filename` | **required** | string | The relative path of the file | "driver/C_20180522_1503_J_00000008771420.DDD" |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`

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

## GET `/tachographs/driving-times`

**Retrieve Tachograph Driver Driving Times**

This endpoint retrieves tachograph driver driving times for a specified date range, providing the total driving and rest times per driver per day within that range.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[driver_ids]` | optional | string | The list of unique identifiers (UUIDs) of the tachograph drivers whose driving times are being requested, separated by commas. | "550e8400-e29b-41d4-a716-446655440000,660e8400-e29b-41d4-a716-446655440111" |
| `filter[start_date]` | optional | `date-only` | The start date for the driving times being requested, default is today. Format: YYYY-MM-DD.    Note: The maximum date range allowed is 1 month. | "2022-01-01" |
| `filter[end_date]` | optional | `date-only` | The end date for the driving times being requested, default is today. Format: YYYY-MM-DD.    Note: The maximum date range allowed is 1 month. | "2022-01-01" |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  | List of driving times. |  |
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

