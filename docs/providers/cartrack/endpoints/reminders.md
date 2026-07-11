---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Reminders
spec_version: 1.26.0622.1
---

# Reminders

_1 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [GET `/reminders/fleet`](#get-reminders-fleet) — Get Fleet Reminders

## GET `/reminders/fleet`

**Get Fleet Reminders**

This endpoint returns all/filtered list of reminders for the fleet.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[vehicle_id]` | optional | integer | Optional filter to retrieve by vehicle ID. | 12345 |
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
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

