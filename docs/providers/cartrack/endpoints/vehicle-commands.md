---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Vehicle Commands
spec_version: 1.26.0622.1
---

# Vehicle Commands

_4 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [GET `/vehicles/immobilise/status`](#get-vehicles-immobilise-status) — Get Immobilise Status for All Vehicles
- [PUT `/vehicles/{registration}/immobilise`](#put-vehicles-registration-immobilise) — Immobilise a vehicle by blocking ignition
- [PUT `/vehicles/{registration}/central-locking`](#put-vehicles-registration-central-locking) — Send command to lock or unlock a vehicle
- [POST `/vehicles/commands/{registration}`](#post-vehicles-commands-registration) — Send command to sound horn or turn on hazard lights on vehicle

## GET `/vehicles/immobilise/status`

**Get Immobilise Status for All Vehicles**

This function requires a specific fitment by Cartrack. To retrieve the Immobilise Status for All Vehicles.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
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

## PUT `/vehicles/{registration}/immobilise`

**Immobilise a vehicle by blocking ignition**

This function requires a specific fitment by Cartrack. When immobilising the vehicle, the user won't be able to power on the vehicle. If the vehicle's engine is already on, the immobilisation will be effective after the current ignition cycle.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `registration` | **required** | `registration` | The license plate of the vehicle | ABC1234 |

### Request Body

The json data that needs to be processed

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `immobilise` | boolean | **required** | Send true to immobilise the vehicle. Send false to release the vehicle. | true |

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  |  |  |
| `meta` | string | null |  |  |  |

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

## PUT `/vehicles/{registration}/central-locking`

**Send command to lock or unlock a vehicle**

This function requires a specific fitment by Cartrack. You can remotely lock or unlock the vehicle by sending the commands "UNLOCK", "UNLOCK_0", "LOCK". A device is installed in your vehicle and reproduces the keyfob signals.

  - "UNLOCK": this command turns on the keyfob in the car and uses it to send the unlock command. The doors can now be opened. The keyfob stays powered up, the user can turn on the engine.
  - "UNLOCK_0": this command turns on the keyfob in the car and uses it to send a different set of unlock command. The doors can now be opened. However, the keyfob will be immediately powered down, the user won't be able to turn on the engine.
  - "LOCK": this command turns on the keyfob in the vehicle and it uses it to send a lock command. The doors are now locked. The keyfob is powered down immediately after, the engine can't be turned on.

Not all vehicles and devices are compatible with this function.

To prevent duplicate commands, the same command cannot be sent to the same vehicle while a previous one is still being processed (up to 30 seconds). In that case the API responds with an HTTP 409 error and a `Retry-After` header indicating the number of seconds to wait before retrying.


### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `registration` | **required** | `registration` | The license plate of the vehicle | ABC1234 |

### Request Body

The json data that needs to be processed

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `command` | string | **required** | The vehicle's lock status | "LOCK" |

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  |  |  |
| `meta` | string | null |  |  | "Instruction has been sent to the Cartrack terminal." |

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

#### `409` — A command of the same type was recently sent to this vehicle and is still being processed. Wait for the number of seconds indicated in the `Retry-After` header before sending the command again.

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

## POST `/vehicles/commands/{registration}`

**Send command to sound horn or turn on hazard lights on vehicle**

This function requires a specific fitment by Cartrack. You can remotely sound the horn on the vehicle or turn on the hazard lights by sending the commands "HORN", "HAZARD_LIGHTS". A device is installed in your vehicle and reproduces the keyfob signals.

  - "HORN": this command turns on the keyfob in the car and uses it to send the horn command.
  - "HAZARD_LIGHTS": this command turns on the keyfob in the car and uses it to send the hazard lights command.

Not all vehicles and devices are compatible with this function.

To prevent duplicate commands, the same command cannot be sent to the same vehicle while a previous one is still being processed (up to 30 seconds). In that case the API responds with an HTTP 409 error and a `Retry-After` header indicating the number of seconds to wait before retrying.


### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `registration` | **required** | `registration` | The license plate of the vehicle | ABC1234X |

### Request Body

The json data that needs to be processed

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `command` | string | **required** | The vehicle's action triggered | "HORN" |

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  |  |  |
| `meta` | string | null |  |  |  |

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

#### `409` — A command of the same type was recently sent to this vehicle and is still being processed. Wait for the number of seconds indicated in the `Retry-After` header before sending the command again.

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

