---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Notifications
spec_version: 1.26.0622.1
---

# Notifications

_1 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [GET `/notifications`](#get-notifications) — [Deprecated] Get your fleet account's notifications

## GET `/notifications`

**[Deprecated] Get your fleet account's notifications**

DEPRECIATION NOTICE: This endpoint has moved, please use GET /alerts/notifications

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[date_from]` | optional | `date` | Start date and time for filtering breakdown entries. Only entries recorded on or after this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[date_to]` | optional | `date` | End date and time for filtering breakdown entries. Only entries recorded on or before this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `filter[contact_type]` | optional | string | Filter by contact type | E-Mail Message |
| `filter[alert_type]` | optional | string | Filter alert trigger description | IGNITION_ON_OFF |
| `filter[status]` | optional | string | Filter by alert status | Email sent |
| `filter[notification_contact]` | optional | string | Filter alert by notification contact | mark.steven@example.com |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |
| `sort_by` | optional | string | The field to sort the results by. Prefix with '-' for descending order. | create_ts |
| `sort_order` | optional | string | The order to sort the results. Can be 'asc' for ascending or 'desc' for descending. | desc |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  | This array returns the list of notifications |  |
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

