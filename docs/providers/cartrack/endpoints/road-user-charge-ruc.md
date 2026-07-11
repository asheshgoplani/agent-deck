---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Road User Charge (RUC)
spec_version: 1.26.0622.1
---

# Road User Charge (RUC)

_1 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [GET `/ruc/latest`](#get-ruc-latest) — Retrieve Vehicle Latest RUC License Record

## GET `/ruc/latest`

**Retrieve Vehicle Latest RUC License Record**

This endpoint returns the latest RUC license record for the vehicles in the fleet.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `filter[license]` | optional | string | null | Optional filter to retrieve records by license | "745381999" |
| `filter[weight]` | optional | string | null | Optional filter to retrieve license records by weight | "2c" |
| `filter[permit_type]` | optional | string | null | Optional filter to retrieve license records by permit_type | "D" |
| `filter[is_valid]` | optional | boolean | null | Optional filter to filter the license records by its validity | true |
| `filter[issue_timestamp_from]` | optional | `date` | Optional filter to retrieve license records on or after a specific date and time. | "2024-09-01 00:00:00" |
| `filter[issue_timestamp_to]` | optional | `date` | Optional filter to retrieve license records on or before a specific date and time. | "2024-09-31 23:59:59" |
| `page` | optional | integer | The current page | 1 |
| `limit` | optional | integer | The number of items to display per page | 15 |

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

