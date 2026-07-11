---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Delivery Reports
spec_version: 1.26.0622.1
---

# Delivery Reports

_1 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [GET `/delivery/reports/drivers`](#get-delivery-reports-drivers) — Generate Delivery Drivers Report

## GET `/delivery/reports/drivers`

**Generate Delivery Drivers Report**

This endpoint retrieves reporting data related to delivery drivers.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[date_from_ts]` | **required** | `date` | Filter starting timestamp range of the driver report date. | 2024-12-01 00:00:00 |
| `filter[date_to_ts]` | **required** | `date` | Filter end timestamp range of the driver report date. | 2024-12-31 23:59:59 |
| `filter[driver_id]` | optional | string | Optional filter to retrieve drivers by their ID. | 62462fcf-0938-11ec-8c4d-a4bf016cd6b2 |
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

