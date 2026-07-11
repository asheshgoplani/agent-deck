---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Drivers
spec_version: 1.26.0622.1
---

# Drivers

_8 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [GET `/drivers`](#get-drivers) — Get All Drivers
- [POST `/drivers`](#post-drivers) — Add Driver
- [GET `/drivers/{driver_id}`](#get-drivers-driver-id) — Get Driver
- [PUT `/drivers/{driver_id}`](#put-drivers-driver-id) — Update Driver
- [GET `/drivers/status/history`](#get-drivers-status-history) — Get Driver Status History
- [GET `/drivers/tags/events`](#get-drivers-tags-events) — Get Driver Tags Events
- [POST `/drivers/{driver_id}/groups/{group_id}`](#post-drivers-driver-id-groups-group-id) — Add Driver to a Group
- [DELETE `/drivers/{driver_id}/groups/{group_id}`](#delete-drivers-driver-id-groups-group-id) — Remove Driver from a Group

## GET `/drivers`

**Get All Drivers**

This endpoint gets all/filtered list of the drivers

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[driver_id]` | optional | string | Filter by driver id (exact match). | 02870802-xxxx-42ed-xxxx-c999df353f42 |
| `filter[first_name]` | optional | string | Filter by first name (partial match). | Mark |
| `filter[last_name]` | optional | string | Filter by last name (partial match). | Steven |
| `filter[email]` | optional | string | Filter by email (partial match). | mark.steven@hotmail.com |
| `filter[id_number]` | optional | string | Filter by ID number (partial match). | 123456 |
| `filter[phone_number]` | optional | string | Filter by phone number (partial match). | 08001234 |
| `filter[gender]` | optional | string | Filter by gender (exact match). | Male |
| `filter[license_number]` | optional | string | Filter by license number (partial match). | ABC123 |
| `filter[license_issued_country]` | optional | string | Filter license by the issuing country (exact match). | SG |
| `filter[license_driver_restrictions]` | optional | string | Filter by driver restriction (partial match). | Weekdays only |
| `filter[license_points]` | optional | integer | Filter by license points (exact match). | 10 |
| `filter[license_first_issue_date]` | optional | string | Filter by license first issue date (exact match). | 2022-01-12 |
| `filter[license_valid_start]` | optional | string | Filter by license start date (exact match). | 2022-01-13 |
| `filter[license_valid_end]` | optional | string | Filter license end date (exact match). | 2035-01-01 |
| `filter[status]` | optional | string | Filter by driver status (exact match). | Active |
| `filter[employee_number]` | optional | string | Filter by employee number (exact match). | 123456 |
| `filter[group_id]` | optional | integer | Filter by group ID |  |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `driver-response` |  |  |  |
| `meta` | `pagination` |  |  |  |

**Content-Type:** `application/xml`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `driver` |  |  |  |
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

## POST `/drivers`

**Add Driver**

This endpoint creates a driver with status Active

### Request Body

The json data that needs to be processed

**Content-Type:** `application/json`




### Responses

#### `200` — Driver created successfully

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | `driver` |  |  |  |

**Content-Type:** `application/xml`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | `driver` |  |  |  |

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

## GET `/drivers/{driver_id}`

**Get Driver**

This endpoint gets the driver details

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `driver_id` | **required** | string | The driver_id you want to get | 06333436-6fe9-11ef-98f4-f241fc6d518a |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`




**Content-Type:** `application/xml`




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

## PUT `/drivers/{driver_id}`

**Update Driver**

This endpoint updates the driver's details

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `driver_id` | **required** | string | The driver_id you want to update | 06333436-6fe9-11ef-98f4-f241fc6d518a |

### Request Body

The json data that needs to be processed

**Content-Type:** `application/json`




### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`




**Content-Type:** `application/xml`




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

## GET `/drivers/status/history`

**Get Driver Status History**

Returns a list of driver status periods. Each record includes driver_id, active_from, and active_to. If the driver is currently active, active_to will be null.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[driver_id]` | optional | string | Filter by driver id (exact match). | 02870802-xxxx-42ed-xxxx-c999df353f42 |
| `filter[active_from]` | optional | `date` | Filter by driver active start date. Must be in the format of YYYY-MM-DD HH:MM:SS | 2025-07-21 10:31:30 |
| `filter[active_to]` | optional | `date` | Filter by driver active end date. Must be in the format of YYYY-MM-DD HH:MM:SS | 2025-07-21 10:31:30 |
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

**Content-Type:** `application/xml`


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

## GET `/drivers/tags/events`

**Get Driver Tags Events**

This endpoint retrieves driver tagging events with timestamp filtering and audit capabilities

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

## POST `/drivers/{driver_id}/groups/{group_id}`

**Add Driver to a Group**

Adds a driver to a driver group.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `driver_id` | **required** | string | The ID of the driver to add to the group | 123e4567-e89b-12d3-a456-426614174000 |
| `group_id` | **required** | integer | The ID of the driver group | 2870802 |

### Request Body

_No request body._

### Responses

#### `200` — Driver added to group successfully

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `meta` | object |  | The metadata such as result messages. |  |

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

## DELETE `/drivers/{driver_id}/groups/{group_id}`

**Remove Driver from a Group**

Removes a driver from a driver group.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `driver_id` | **required** | string | The ID of the driver to remove from the group | 123e4567-e89b-12d3-a456-426614174000 |
| `group_id` | **required** | integer | The ID of the driver group | 2870802 |

### Request Body

_No request body._

### Responses

#### `200` — Driver removed from group successfully

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `meta` | object |  | The metadata such as result messages. |  |

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

