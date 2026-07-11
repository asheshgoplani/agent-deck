---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Driver Groups
spec_version: 1.26.0622.1
---

# Driver Groups

_8 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [GET `/drivers/groups`](#get-drivers-groups) — Get All Driver Groups
- [POST `/drivers/groups`](#post-drivers-groups) — Create Driver Group
- [GET `/drivers/groups/{group_id}`](#get-drivers-groups-group-id) — Get Driver Group Details
- [PUT `/drivers/groups/{group_id}`](#put-drivers-groups-group-id) — Update Driver Group Details
- [DELETE `/drivers/groups/{group_id}`](#delete-drivers-groups-group-id) — Delete Driver Group
- [GET `/drivers/groups/{group_id}/drivers`](#get-drivers-groups-group-id-drivers) — Get All Drivers in a Group
- [POST `/drivers/groups/{group_id}/drivers`](#post-drivers-groups-group-id-drivers) — Add Drivers to a Group
- [DELETE `/drivers/groups/{group_id}/drivers`](#delete-drivers-groups-group-id-drivers) — Remove Drivers from a Group

## GET `/drivers/groups`

**Get All Driver Groups**

Fetches a list of driver groups, with optional filtering and sorting.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[group_id]` | optional | integer | Filter by driver group id (exact match). | 2870802 |
| `filter[name]` | optional | string | Filter by driver group name (partial match). | Mark |
| `filter[driver_id]` | optional | string | Filter by driver id (exact match). | 123e4567-e89b-12d3-a456-426614174000 |
| `sort_by` | optional | string | The field to sort the results by. Allowed values are 'name' and 'group_driver_id'. Default is 'name'. |  |
| `sort_order` | optional | string | The order to sort the results. Allowed values are 'asc' for ascending and 'desc' for descending. Default is 'asc'. |  |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `driver-group-response` |  |  |  |
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

## POST `/drivers/groups`

**Create Driver Group**

Creates a new driver group. Optionally provide driver_ids to populate the group on creation.

### Request Body

JSON payload for the driver group to create

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `name` | string | **required** | The name of the driver group | "Mark's Group" |
| `description` | string |  | The description of the driver group | "This is Mark's driver group" |
| `driver_ids` | array of string |  | The list of driver ids to be added to the driver group | ["123e4567-e89b-12d3-a456-426614174000", "123e4567-e89b-12d3-a456-426614174001"] |

### Responses

#### `200` — Driver group created successfully

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | `driver-group-response` |  |  |  |

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

## GET `/drivers/groups/{group_id}`

**Get Driver Group Details**

Retrieves the details of a driver group by ID.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `group_id` | **required** | integer | The ID of the driver group you want to get | 2870802 |

### Request Body

_No request body._

### Responses

#### `200` — Driver group details retrieved successfully

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | `driver-group-response` |  |  |  |

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

## PUT `/drivers/groups/{group_id}`

**Update Driver Group Details**

Updates the details of a driver group by ID.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `group_id` | **required** | integer | The ID of the driver group you want to update | 2870802 |

### Request Body

JSON payload with the fields to update

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `name` | string | **required** | The name of the driver group | "Mark's Group" |
| `description` | string |  | The description of the driver group | "This is Mark's driver group" |
| `driver_ids` | array of string |  | The list of driver ids to be added to the driver group | ["123e4567-e89b-12d3-a456-426614174000", "123e4567-e89b-12d3-a456-426614174001"] |

### Responses

#### `200` — Driver group details updated successfully

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | `driver-group-response` |  |  |  |

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

## DELETE `/drivers/groups/{group_id}`

**Delete Driver Group**

Deletes a driver group by ID.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `group_id` | **required** | integer | The ID of the driver group you want to delete | 2870802 |

### Request Body

_No request body._

### Responses

#### `200` — Driver group deleted successfully

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

## GET `/drivers/groups/{group_id}/drivers`

**Get All Drivers in a Group**

Fetches a paginated list of drivers belonging to the specified group.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `group_id` | **required** | integer | The ID of the driver group you want to get the drivers from | 2870802 |

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[driver_id]` | optional | string | Filter by driver id (exact match). | 123e4567-e89b-12d3-a456-426614174000 |
| `filter[driver_name]` | optional | string | Filter by driver name (partial match). | John |
| `filter[email]` | optional | string | Filter by driver email (partial match). | john.doe@example.com |
| `filter[phone_number]` | optional | string | Filter by driver phone number (partial match). | 84-905876743 |
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

## POST `/drivers/groups/{group_id}/drivers`

**Add Drivers to a Group**

Adds one or more drivers to a driver group. Drivers already in the group are ignored. Returns 422 if all submitted driver IDs are already in the group.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `group_id` | **required** | integer | The ID of the driver group you want to add drivers to | 2870802 |

### Request Body

JSON payload containing the list of driver IDs to add

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `driver_ids` | array of string | **required** | The list of driver IDs to be added to the group |  |

### Responses

#### `200` — Drivers added to group successfully

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  |  |  |
| `meta` | object |  |  |  |

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

## DELETE `/drivers/groups/{group_id}/drivers`

**Remove Drivers from a Group**

Removes one or more drivers from a driver group. Set delete_all to true to remove all drivers, or provide driver_ids for selective removal.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `group_id` | **required** | integer | The ID of the driver group you want to remove drivers from | 2870802 |

### Request Body

JSON payload specifying which drivers to remove

**Content-Type:** `application/json`


_Schema: object_

### Responses

#### `200` — Drivers removed from group successfully

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `meta` | object |  |  |  |

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

