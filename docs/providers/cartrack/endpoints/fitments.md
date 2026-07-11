---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Fitments
spec_version: 1.26.0622.1
---

# Fitments

_1 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [GET `/fitments`](#get-fitments) — Get All Fitments

## GET `/fitments`

**Get All Fitments**

This endpoints returns the latest fitments or repairs of a Cartrack tracker/digital video recorder, using this endpoint.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `start_date` | **required** | `date` | This will return fitments that happened after this date and time | 2023-01-01 12:00:00 |
| `end_date` | **required** | `date` | This will return fitments that happened before this date and time | 2023-01-01 12:00:00 |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  | This array returns the list of fitments or repairs |  |
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

