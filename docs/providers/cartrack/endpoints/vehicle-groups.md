---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Vehicle Groups
spec_version: 1.26.0622.1
---

# Vehicle Groups

_6 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [GET `/vehicles/groups`](#get-vehicles-groups) — Retrieve Vehicle Groups
- [POST `/vehicles/groups`](#post-vehicles-groups) — Create New Vehicle Group
- [PUT `/vehicles/groups/{group_id}`](#put-vehicles-groups-group-id) — Update Existing Vehicle Group
- [DELETE `/vehicles/groups/{group_id}`](#delete-vehicles-groups-group-id) — Remove Vehicle Group
- [PUT `/vehicles/{registration}/groups/{group_id}`](#put-vehicles-registration-groups-group-id) — Assign Vehicle to Group
- [DELETE `/vehicles/{registration}/groups/{group_id}`](#delete-vehicles-registration-groups-group-id) — Remove Vehicle from Group

## GET `/vehicles/groups`

**Retrieve Vehicle Groups**

Fetches a list of available vehicle groups along with their details.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `filter[name]` | optional | string | Filter by group name, case insensitive, can be partial match | "Good looking vehicles" |
| `filter[description]` | optional | string | Filter by group description, case insensitive, can be partial match | "All active vehicles" |
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

## POST `/vehicles/groups`

**Create New Vehicle Group**

Adds a new vehicle group to the system with the provided details.

### Request Body

One or more of the parameters to be created

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `name` | string | **required** | The name of the new vehicle group. | "Luxury Cars Club" |
| `description` | string | null |  | A brief description of the new vehicle group. | "A community for enthusiasts of high-end and luxury cars." |
| `subuser_id` | string | null |  | The unique identifier of the subuser to whom the vehicle group is assigned. | "62462fcf-0938-11ec-8c4d-a4bf016cd6b2" |

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  | Details about the newly created group |  |

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

## PUT `/vehicles/groups/{group_id}`

**Update Existing Vehicle Group**

Modifies the details of an existing vehicle group based on the provided information.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `group_id` | **required** | integer | The vehicle group ID | 1010 |

### Request Body

One or more of the parameters to be updated

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `name` | string |  | The updated name of the vehicle group. | "Group of vintage cars manufactured before 1990." |
| `description` | string | null |  | The updated description of the vehicle group. | "This group includes cars with historical significance." |
| `subuser_id` | string | null |  | The updated subuser ID associated with the vehicle group. | "62462fcf-0938-11ec-8c4d-a4bf016cd6b2" |

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  | Details about the newly created group |  |

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

## DELETE `/vehicles/groups/{group_id}`

**Remove Vehicle Group**

Deletes a specific vehicle group from the system.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `group_id` | **required** | integer | The vehicle group ID | 1010 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object | null |  |  | {} |
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

## PUT `/vehicles/{registration}/groups/{group_id}`

**Assign Vehicle to Group**

Associates a specific vehicle with a designated vehicle group, using the vehicle's registration number and the group's unique ID.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `registration` | **required** | `registration` | The vehicle's license plate | "ABC1234X" |
| `group_id` | **required** | integer | The vehicle group ID | 1010 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object | null |  |  |  |
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

## DELETE `/vehicles/{registration}/groups/{group_id}`

**Remove Vehicle from Group**

Removes a specified vehicle from an assigned group, using the vehicle's registration number and the group's unique ID.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `registration` | **required** | `registration` | The vehicle's license plate | "ABC1234X" |
| `group_id` | **required** | integer | The vehicle group ID | 1010 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object | null |  |  |  |
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

