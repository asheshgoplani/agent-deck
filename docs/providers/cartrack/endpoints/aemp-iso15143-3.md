---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: AEMP ISO15143-3
spec_version: 1.26.0622.1
---

# AEMP ISO15143-3

_2 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [GET `/aemp/iso15143-3/beta/equipment/{id}`](#get-aemp-iso15143-3-beta-equipment-id) — Equipment Snapshot
- [GET `/aemp/iso15143-3/beta/fleet`](#get-aemp-iso15143-3-beta-fleet) — Fleet Snapshot

## GET `/aemp/iso15143-3/beta/equipment/{id}`

**Equipment Snapshot**

This API follows the ISO 15143-3 standard (AEMP Telematics Standard), which defines a common data format for sharing telematics data across mixed equipment fleets, enabling interoperability between different manufacturers and systems. It is Cartrack's first version and currently in beta testing. Gets the snapshot of the specific equipment (vehicle) data.


### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | string | The vehicle's registration or equipment identifier | TESTRUCEESG |

### Header Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `Accept` | **required** | string | The Accept header specifying the response format - Use 'application/iso15143-snapshot+json' for ISO 15143-3 format | application/iso15143-snapshot+json |

### Request Body

_No request body._

### Responses

#### `200` — Equipment snapshot information response

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `EquipmentHeader` | `aemp-equipment-header` | **required** |  |  |
| `Location` | `aemp-location` |  | The last known vehicle location |  |
| `FuelUsed` | object |  | Fuel consumed by the vehicle |  |
| `Distance` | object |  | Distance travelled by the vehicle |  |
| `FuelRemaining` | object |  | Fuel remaining percentage of the vehicle |  |
| `EngineStatus` | object |  | Engine status of the vehicle |  |
| `CumulativeOperatingHours` | object |  | The current total cumulative operating hours of the vehicle |  |
| `CumulativeIdleHours` | object |  | The cumulative idle non-operating hours of the vehicle |  |

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

## GET `/aemp/iso15143-3/beta/fleet`

**Fleet Snapshot**

This API follows the ISO 15143-3 standard (AEMP Telematics Standard), which defines a common data format for sharing telematics data across mixed equipment fleets, enabling interoperability between different manufacturers and systems. Gets the fleet snapshot of the latest available equipments (vehicles) data. Response is limited to a maximum of 100 records per page.


### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Header Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `Accept` | **required** | string | The Accept header specifying the response format - Use 'application/iso15143-snapshot+json' for ISO 15143-3 format | application/iso15143-snapshot+json |

### Request Body

_No request body._

### Responses

#### `200` — Fleet snapshot response with pagination

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `Equipment` | array of object | **required** | List of equipments (vehicles). |  |
| `Links` | array of object | **required** |  | [{"Rel": "Current", "Href": "https://api.cartrack.com/aemp/iso15143-3/beta/fleet?page=1"}, {"Rel": "Next", "Href": "https://api.cartrack.com/aemp/iso15143-3/beta/fleet?page=2"}, {"Rel": "Last", "Href": "https://api.cartrack.com/aemp/iso15143-3/beta/fleet?page=10"}, {"Rel": "First", "Href": "https://api.cartrack.com/aemp/iso15143-3/beta/fleet?page=1"}] |

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

