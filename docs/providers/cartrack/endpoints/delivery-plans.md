---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Delivery Plans
spec_version: 1.26.0622.1
---

# Delivery Plans

_2 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [POST `/delivery/plans`](#post-delivery-plans) — Create Delivery Plan
- [DELETE `/delivery/plans/{plan_id}`](#delete-delivery-plans-plan-id) — Delete Delivery Plan

## POST `/delivery/plans`

**Create Delivery Plan**

This endpoint creates a new delivery plan.

### Request Body

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `name` | string | **required** | The name of the delivery plan. | "Delivery Plan #0701" |
| `delivery_driver_id` | string | null |  | The ID of the delivery driver to assign to the plan. | "f3a42187-0c6e-11ec-aa41-a4bf016cd6b2" |
| `scheduled_delivery_ts` | string |  | The date and time of the scheduled delivery. | "2025-07-01 12:00:00" |

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `plan_id` |  | **required** |  |  |
| `name` | string | **required** | The name of the delivery plan. | "Delivery Plan #0701" |
| `delivery_driver_id` | string | null |  | The ID of the delivery driver to assign to the plan. | "f3a42187-0c6e-11ec-aa41-a4bf016cd6b2" |
| `scheduled_delivery_ts` | string |  | The date and time of the scheduled delivery. | "2025-07-01 12:00:00" |

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

## DELETE `/delivery/plans/{plan_id}`

**Delete Delivery Plan**

This endpoint deletes a delivery plan.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `plan_id` | **required** | `plan-id` | The ID of the delivery plan to delete. | 12345 |

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

