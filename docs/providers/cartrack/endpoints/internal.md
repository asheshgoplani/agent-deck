---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: internal
spec_version: 1.26.0622.1
---

# internal

_1 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [POST `/delivery/optimize/routes`](#post-delivery-optimize-routes) — Delivery Jobs Optimization

## POST `/delivery/optimize/routes`

**Delivery Jobs Optimization**

This endpoint suggests delivery job assignments to available drivers based on selected optimization constraints. The system intelligently matches jobs to drivers to improve delivery efficiency while considering rules such as time windows and equipment requirements. The suggested assignments are recommendations only and do not automatically assign the jobs to drivers.

### Request Body

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `job_ids` | array of integer | **required** | Array of job IDs to be optimized |  |
| `driver_ids` | array of string | **required** | Array of driver IDs available for assignment |  |
| `enable_capacity_constraints` | boolean | null |  | Whether to enable capacity constraints. |  |
| `enable_time_window_constraints` | boolean | null |  | Whether to enable time window constraints. |  |
| `enable_equipment_constraints` | boolean | null |  | Whether to enable equipment constraints. |  |
| `version` | string | null |  | The version of the optimization algorithm model to use. Default is v1 if not specified. | "v1" |

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  |  |  |

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

