---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Delivery Special Equipment
spec_version: 1.26.0622.1
---

# Delivery Special Equipment

_1 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [GET `/delivery/equipment`](#get-delivery-equipment) — Get Delivery Special Equipment

## GET `/delivery/equipment`

**Get Delivery Special Equipment**

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[equipment_id]` | optional | number | Filter by equipment ID | 10 |
| `filter[equipment_name]` | optional | string | Filter by equipment name | Pallet Jack |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Special equipment list retrieved successfully

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

