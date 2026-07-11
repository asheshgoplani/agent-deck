---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Coaching
spec_version: 1.26.0622.1
---

# Coaching

_1 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [GET `/coaching/events`](#get-coaching-events) — [BETA] Get coaching events

## GET `/coaching/events`

**[BETA] Get coaching events**

This endpoint retrieves coaching events for your account within a specified date range. Please note that this API is in beta, and its behavior may change. Exercise caution when using it.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
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
| `data` | array of object |  | List of coaching events. |  |
| `meta` | `pagination` |  |  |  |

**Content-Type:** `application/xml`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  | List of coaching events. |  |
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

