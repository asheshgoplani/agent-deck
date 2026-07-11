---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Geofence Groups
spec_version: 1.26.0622.1
---

# Geofence Groups

_6 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [GET `/geofences/groups`](#get-geofences-groups) — Retrieve Geofence Groups
- [POST `/geofences/groups`](#post-geofences-groups) — Create Geofence Group
- [PUT `/geofences/groups/{group_id}`](#put-geofences-groups-group-id) — Update Geofence Group
- [DELETE `/geofences/groups/{group_id}`](#delete-geofences-groups-group-id) — Remove Geofence Group
- [PUT `/geofences/{geofence_id}/groups/{group_id}`](#put-geofences-geofence-id-groups-group-id) — Assign Geofence to Group
- [DELETE `/geofences/{geofence_id}/groups/{group_id}`](#delete-geofences-geofence-id-groups-group-id) — Remove Geofence from Group

## GET `/geofences/groups`

**Retrieve Geofence Groups**

Fetches a list of all geofence groups, providing detailed information for each group.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[group_id]` | optional | integer | The geofence group ID | 1010 |
| `filter[geofence_id]` | optional | string | Filter groups that contains a geofence ID, an exact match | "62462fcf-0938-11ec-8c4d-a4bf016cd6b2" |
| `filter[name]` | optional | string | Filter by group name, case insensitive, can be partial match | "Office building geofences" |
| `filter[description]` | optional | string | Filter by group description, case insensitive, can be partial match | "All active geofences" |
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

## POST `/geofences/groups`

**Create Geofence Group**

Creates a new geofence group, allowing for the grouping of multiple geofences under a single identifier.

### Request Body

JSON payload containing the data required for creating a geofence group

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `name` | `group-name` | **required** |  |  |
| `description` | `group-description` |  | Add a description for this new geofence group |  |
| `subuser_id` | string |  | Add a subuser_id the group must be assigned to for a specific subuser | "62462fcf-0938-11ec-8c4d-a4bf016cd6b2" |

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  |  |  |

#### `400` — Validation failed for the input parameters.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

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

## PUT `/geofences/groups/{group_id}`

**Update Geofence Group**

Updates the details of an existing geofence group, identified by the group's unique ID.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `group_id` | **required** | `group-id` |  | 1234 |

### Request Body

One or more of the parameters to be updated.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `name` | `group-name` |  | The updated name of the group |  |
| `description` | `group-description` |  | The updated description of the group |  |
| `subuser_id` | string |  | The updated subuser_id | "62462fcf-0938-11ec-8c4d-a4bf016cd6b2" |

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

## DELETE `/geofences/groups/{group_id}`

**Remove Geofence Group**

Removes an existing geofence group from the system, identified by the group's unique ID.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `group_id` | **required** | `group-id` | The ID of the geofence group to delete | 1234 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  |  | {} |
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

## PUT `/geofences/{geofence_id}/groups/{group_id}`

**Assign Geofence to Group**

Associates a specific geofence with a designated geofence group, using the geofence's unique ID and the group's ID.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `geofence_id` | **required** | string |  | 62462fcf-0938-11ec-8c4d-a4bf016cd6b2 |
| `group_id` | **required** | `group-id` |  | 1234 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  |  | {} |
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

## DELETE `/geofences/{geofence_id}/groups/{group_id}`

**Remove Geofence from Group**

Removes a specified geofence from a designated group, identified by the geofence's unique ID and the group's ID.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `geofence_id` | **required** | string |  | 62462fcf-0938-11ec-8c4d-a4bf016cd6b2 |
| `group_id` | **required** | `group-id` | The ID of the geofence group | 1234 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  |  | {} |
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

