---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Terminal Commands
spec_version: 1.26.0622.1
---

# Terminal Commands

_4 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [GET `/terminal/config1`](#get-terminal-config1) — Get DID, immobilisation and buzzer configuration for a list of vehicles (config 1)
- [PUT `/terminal/config1`](#put-terminal-config1) — Update the config 1 for a list of vehicles
- [GET `/terminal/config9`](#get-terminal-config9) — Get overspeed configuration for a list of vehicles (config9)
- [PUT `/terminal/config9`](#put-terminal-config9) — Update the config 9 for a list of vehicles

## GET `/terminal/config1`

**Get DID, immobilisation and buzzer configuration for a list of vehicles (config 1)**

This function returns the current configuration (with 1 = ON, 0 = OFF) for the following:
  - Driver identification toggle
  - Immobilisation toggle
  - Buzzer toggle

You can use this endpoint by passing the list of identifiers (comma separated) inside the query parameter `terminal_identifiers` with either:
  - the vehicles' identification numbers (VIN)
  - the Cartrack terminals' serials
  - the vehicles' registrations

*Important: All identifiers in the list must be of the same type.*


### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `terminal_identifiers` | **required** | string | The terminal identifiers, comma separated. They must be of the same type | "ABC123X" |

### Request Body

_No request body._

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

## PUT `/terminal/config1`

**Update the config 1 for a list of vehicles**

Please use this function to toggle on/off:
  - the driver identification
  - the vehicle immobilisation
  - the buzzer

The expected values are: **ON**, **OFF**, **UNCHANGED**

You can use this endpoint by passing the list of identifiers (comma separated) inside the query parameter `terminal_identifiers` with either:
  - the vehicles' identification numbers (VIN)
  - the Cartrack terminals' serials
  - the vehicles' registrations

*Important: All identifiers in the list must be of the same type.*

**Notice:**
1. After making a call, please verify the returned value `configuration_ts` to make sure the configuration is received by the vehicle.
2. This endpoint should ***not be called more than once every 5 minutes*** to prevent hardware failures.


### Request Body

The json data that needs to be processed

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `terminal_identifiers` | string | **required** | The terminal identifiers, comma seperated. They must be of the same type | "ABC123X" |
| `did_toggle` | string | **required** | The driver identification tag - for drivers to identify themselves with a tag - active/inactive | "ON" |
| `immobilisation_toggle` | string | **required** | Immobilisation of the vehicle - if active, it won't be possible to turn on the engine  - active/inactive | "OFF" |
| `buzzer_toggle` | string | **required** | Buzzer in the vehicle - if active and the driver is not identified, it will buzz - active/inactive | "OFF" |

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

## GET `/terminal/config9`

**Get overspeed configuration for a list of vehicles (config9)**

This function returns the current configuration (with 1 = ON, 0 = OFF) for the following:
  - Overspeed buzzer thresholds in km/h
  - Buzzer toggle

You can use this endpoint by passing the list of identifiers (comma separated) inside the query parameter `terminal_identifiers` with either:
  - the vehicles' identification numbers (VIN)
  - the Cartrack terminals' serials
  - the vehicles' registrations

*Important: All identifiers in the list must be of the same type.*


### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `terminal_identifiers` | **required** | string | The terminal identifiers, comma separated. They must be of the same type | "ABC123X" |

### Request Body

_No request body._

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

## PUT `/terminal/config9`

**Update the config 9 for a list of vehicles**

Please use this function to toggle on/off:
  - the overspeed buzzer threshold
  - the overspeed buzzer toggle

The expected values are: **ON**, **OFF**, **UNCHANGED**

You can use this endpoint by passing the list of identifiers (comma separated) inside the query parameter `terminal_identifiers` with either:
  - the vehicles' identification numbers (VIN)
  - the Cartrack terminals' serials
  - the vehicles' registrations

*Important: All identifiers in the list must be of the same type.*

**Notice:**
1. After making a call, please verify the returned value `configuration_ts` to make sure the configuration is received by the vehicle.
2. This endpoint should ***not be called more than once every 5 minutes*** to prevent hardware failures.


### Request Body

The json data that needs to be processed

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `terminal_identifiers` | string | **required** | The terminal identifiers, comma seperated. They must be of the same type | "ABC123X" |
| `overspeed_threshold` | integer | **required** | The configured overpseed threshold - if the vehicle goes over this limit, it will trigger overspeeding events | 130 |
| `overspeed_buzzer` | string | **required** | The configured overspeed buzzer - if the vehicle goes over the specified speed limit, it will trigger the buzzer | "ON" |

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

