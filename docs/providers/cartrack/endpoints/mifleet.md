---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: MiFleet
spec_version: 1.26.0622.1
---

# MiFleet

_92 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [GET `/mifleet/fuel`](#get-mifleet-fuel) — Retrieve MiFleet Fuel Validation Entries
- [POST `/mifleet/fuel`](#post-mifleet-fuel) — Create a New MiFleet Fuel Validation Entry
- [PUT `/mifleet/fuel/{id}`](#put-mifleet-fuel-id) — Update MiFleet Fuel Entry
- [DELETE `/mifleet/fuel/{id}`](#delete-mifleet-fuel-id) — Delete MiFleet Fuel Entries
- [GET `/mifleet/maintenance`](#get-mifleet-maintenance) — Get MiFleet Maintenance Entries
- [POST `/mifleet/maintenance`](#post-mifleet-maintenance) — Create a New MiFleet Maintenance Entry
- [PUT `/mifleet/maintenance/{id}`](#put-mifleet-maintenance-id) — Update MiFleet Maintenance Entry
- [DELETE `/mifleet/maintenance/{id}`](#delete-mifleet-maintenance-id) — Delete MiFleet Maintenance Entries
- [GET `/mifleet/toll`](#get-mifleet-toll) — Get MiFleet Toll Entries
- [POST `/mifleet/toll`](#post-mifleet-toll) — Create a New MiFleet Toll Validation Entry
- [PUT `/mifleet/toll/{id}`](#put-mifleet-toll-id) — Update MiFleet Toll Validation Entry
- [DELETE `/mifleet/toll/{id}`](#delete-mifleet-toll-id) — Delete MiFleet Toll Entries
- [GET `/mifleet/accident`](#get-mifleet-accident) — Get MiFleet Accident Entries
- [POST `/mifleet/accident`](#post-mifleet-accident) — Create New MiFleet Accident Entries
- [PUT `/mifleet/accident/{id}`](#put-mifleet-accident-id) — Update MiFleet Accident Transaction Entry
- [DELETE `/mifleet/accident/{id}`](#delete-mifleet-accident-id) — Delete MiFleet Accident Entries
- [GET `/mifleet/breakdown`](#get-mifleet-breakdown) — Get MiFleet Breakdown Entries
- [POST `/mifleet/breakdown`](#post-mifleet-breakdown) — Create New MiFleet Breakdown Entry
- [PUT `/mifleet/breakdown/{id}`](#put-mifleet-breakdown-id) — Update MiFleet Breakdown Validation Entry
- [DELETE `/mifleet/breakdown/{id}`](#delete-mifleet-breakdown-id) — Delete MiFleet Breakdown Entries
- [GET `/mifleet/cleaning`](#get-mifleet-cleaning) — Get MiFleet Cleaning Entries
- [POST `/mifleet/cleaning`](#post-mifleet-cleaning) — Create New MiFleet cleaning Entry
- [PUT `/mifleet/cleaning/{id}`](#put-mifleet-cleaning-id) — Update MiFleet Cleaning Entry
- [DELETE `/mifleet/cleaning/{id}`](#delete-mifleet-cleaning-id) — Delete MiFleet Cleaning Entries
- [GET `/mifleet/consumable`](#get-mifleet-consumable) — Get MiFleet Consumable Entries
- [POST `/mifleet/consumable`](#post-mifleet-consumable) — Create New MiFleet Consumable Entries
- [PUT `/mifleet/consumable/{id}`](#put-mifleet-consumable-id) — Update MiFleet Consumable Transaction Entry
- [DELETE `/mifleet/consumable/{id}`](#delete-mifleet-consumable-id) — Delete MiFleet Consumable Entries
- [GET `/mifleet/driver-cost`](#get-mifleet-driver-cost) — Get MiFleet Driver Cost Entries
- [POST `/mifleet/driver-cost`](#post-mifleet-driver-cost) — Create New MiFleet Driver Cost Entries
- [PUT `/mifleet/driver-cost/{id}`](#put-mifleet-driver-cost-id) — Update MiFleet Driver Cost Transaction Entry
- [DELETE `/mifleet/driver-cost/{id}`](#delete-mifleet-driver-cost-id) — Delete MiFleet Driver Cost Entries
- [GET `/mifleet/driver-license`](#get-mifleet-driver-license) — Get MiFleet Driver License Entries
- [POST `/mifleet/driver-license`](#post-mifleet-driver-license) — Create New MiFleet Driver License Entries
- [PUT `/mifleet/driver-license/{id}`](#put-mifleet-driver-license-id) — Update MiFleet Driver License Transaction Entry
- [DELETE `/mifleet/driver-license/{id}`](#delete-mifleet-driver-license-id) — Delete MiFleet Driver License Entries
- [GET `/mifleet/contract/financing`](#get-mifleet-contract-financing) — Get MiFleet Contracts for Financing
- [POST `/mifleet/contract/financing`](#post-mifleet-contract-financing) — Create New MiFleet Financing Contract
- [PUT `/mifleet/contract/financing/{id}`](#put-mifleet-contract-financing-id) — Update MiFleet Financing Contract
- [DELETE `/mifleet/contract/financing/{id}`](#delete-mifleet-contract-financing-id) — Delete MiFleet Financing Contracts
- [GET `/mifleet/financing`](#get-mifleet-financing) — Get MiFleet Financing Entries
- [POST `/mifleet/financing`](#post-mifleet-financing) — Create New MiFleet Financing Entries
- [PUT `/mifleet/financing/{id}`](#put-mifleet-financing-id) — Update MiFleet Financing Transaction Entry
- [DELETE `/mifleet/financing/{id}`](#delete-mifleet-financing-id) — Delete MiFleet Financing Entries
- [GET `/mifleet/fine`](#get-mifleet-fine) — Get MiFleet Fine Entries
- [POST `/mifleet/fine`](#post-mifleet-fine) — Create New MiFleet Fine Validation Entry
- [PUT `/mifleet/fine/{id}`](#put-mifleet-fine-id) — Update MiFleet Fine Validation Entry
- [DELETE `/mifleet/fine/{id}`](#delete-mifleet-fine-id) — Delete MiFleet Fine Entries
- [GET `/mifleet/contract/fuel-card`](#get-mifleet-contract-fuel-card) — Get MiFleet Contracts for Fuel Card
- [POST `/mifleet/contract/fuel-card`](#post-mifleet-contract-fuel-card) — Create New MiFleet Fuel Card Contract
- [PUT `/mifleet/contract/fuel-card/{id}`](#put-mifleet-contract-fuel-card-id) — Update MiFleet Fuel Card Contract
- [DELETE `/mifleet/contract/fuel-card/{id}`](#delete-mifleet-contract-fuel-card-id) — Delete MiFleet Fuel Card Contracts
- [GET `/mifleet/contract/insurance`](#get-mifleet-contract-insurance) — Get MiFleet Contracts for Insurance
- [POST `/mifleet/contract/insurance`](#post-mifleet-contract-insurance) — Create New MiFleet Insurance Contract
- [PUT `/mifleet/contract/insurance/{id}`](#put-mifleet-contract-insurance-id) — Update MiFleet Insurance Contract
- [DELETE `/mifleet/contract/insurance/{id}`](#delete-mifleet-contract-insurance-id) — Delete MiFleet Insurance Contracts
- [GET `/mifleet/insurance`](#get-mifleet-insurance) — Get MiFleet Insurance Entries
- [POST `/mifleet/insurance`](#post-mifleet-insurance) — Create New MiFleet Insurance Entries
- [PUT `/mifleet/insurance/{id}`](#put-mifleet-insurance-id) — Update MiFleet Insurance Transaction Entry
- [DELETE `/mifleet/insurance/{id}`](#delete-mifleet-insurance-id) — Delete MiFleet Insurance Entries
- [GET `/mifleet/leasing-cost`](#get-mifleet-leasing-cost) — Get MiFleet Leasing Cost Entries
- [POST `/mifleet/leasing-cost`](#post-mifleet-leasing-cost) — Create New MiFleet Leasing Cost Entries
- [PUT `/mifleet/leasing-cost/{id}`](#put-mifleet-leasing-cost-id) — Update MiFleet Leasing Cost Transaction Entry
- [DELETE `/mifleet/leasing-cost/{id}`](#delete-mifleet-leasing-cost-id) — Delete MiFleet Leasing Cost Entries
- [GET `/mifleet/contract/maintenance`](#get-mifleet-contract-maintenance) — Get MiFleet Contracts for Maintenance
- [POST `/mifleet/contract/maintenance`](#post-mifleet-contract-maintenance) — Create New MiFleet Contract Maintenance Entries
- [PUT `/mifleet/contract/maintenance/{id}`](#put-mifleet-contract-maintenance-id) — Update MiFleet Maintenance Contract
- [DELETE `/mifleet/contract/maintenance/{id}`](#delete-mifleet-contract-maintenance-id) — Delete MiFleet Maintenance Contracts
- [GET `/mifleet/oil`](#get-mifleet-oil) — Get MiFleet Oil Entries
- [POST `/mifleet/oil`](#post-mifleet-oil) — Create New MiFleet Oil Entries
- [PUT `/mifleet/oil/{id}`](#put-mifleet-oil-id) — Update MiFleet Oil Transaction Entry
- [DELETE `/mifleet/oil/{id}`](#delete-mifleet-oil-id) — Delete MiFleet Oil Entries
- [GET `/mifleet/purchase`](#get-mifleet-purchase) — Get MiFleet Purchase Entries
- [POST `/mifleet/purchase`](#post-mifleet-purchase) — Create New MiFleet Purchase Entries
- [PUT `/mifleet/purchase/{id}`](#put-mifleet-purchase-id) — Update MiFleet Purchase Transaction Entry
- [DELETE `/mifleet/purchase/{id}`](#delete-mifleet-purchase-id) — Delete MiFleet Purchase Entries
- [GET `/mifleet/rental-cost`](#get-mifleet-rental-cost) — Get MiFleet Rental Cost Entries
- [POST `/mifleet/rental-cost`](#post-mifleet-rental-cost) — Create New MiFleet Rental Cost Entries
- [PUT `/mifleet/rental-cost/{id}`](#put-mifleet-rental-cost-id) — Update MiFleet Rental Cost Transaction Entry
- [DELETE `/mifleet/rental-cost/{id}`](#delete-mifleet-rental-cost-id) — Delete MiFleet Rental Cost Entries
- [GET `/mifleet/tax`](#get-mifleet-tax) — Get MiFleet Tax Entries
- [POST `/mifleet/tax`](#post-mifleet-tax) — Create New MiFleet Tax Entries
- [PUT `/mifleet/tax/{id}`](#put-mifleet-tax-id) — Update MiFleet Tax Transaction Entry
- [DELETE `/mifleet/tax/{id}`](#delete-mifleet-tax-id) — Delete MiFleet Tax Entries
- [GET `/mifleet/tyre`](#get-mifleet-tyre) — Get MiFleet Tyre Entries
- [POST `/mifleet/tyre`](#post-mifleet-tyre) — Create New MiFleet Tyre Entries
- [PUT `/mifleet/tyre/{id}`](#put-mifleet-tyre-id) — Update MiFleet Tyre Transaction Entry
- [DELETE `/mifleet/tyre/{id}`](#delete-mifleet-tyre-id) — Delete MiFleet Tyre Entries
- [GET `/mifleet/vehicle-license`](#get-mifleet-vehicle-license) — Get MiFleet Vehicle License Entries
- [POST `/mifleet/vehicle-license`](#post-mifleet-vehicle-license) — Create New MiFleet Vehicle License Entries
- [PUT `/mifleet/vehicle-license/{id}`](#put-mifleet-vehicle-license-id) — Update MiFleet Vehicle License Transaction Entry
- [DELETE `/mifleet/vehicle-license/{id}`](#delete-mifleet-vehicle-license-id) — Delete MiFleet Vehicle License Entries

## GET `/mifleet/fuel`

**Retrieve MiFleet Fuel Validation Entries**

Retrieves a detailed list of fuel entries for MiFleet. This endpoint is designed for comprehensive tracking and management of fuel-related expenses and activities.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[date_from]` | optional | `date` | Start date and time for filtering breakdown entries. Only entries recorded on or after this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[date_to]` | optional | `date` | End date and time for filtering breakdown entries. Only entries recorded on or before this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `filter[id]` | optional | integer | Optional filter to retrieve by identification number. | 67890 |
| `filter[ids]` | optional | string | Filter by multiple entry IDs, separated by commas. Suitable for batch queries. | 67890,67891 |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-fuel-response` |  | This array returns the list of fuel entries. |  |
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

## POST `/mifleet/fuel`

**Create a New MiFleet Fuel Validation Entry**

Creates new fuel validation entries for MiFleet. This endpoint is used for adding detailed fuel transaction records, including purchase amounts, fuel types, and associated vehicle information.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Request Body

JSON payload containing the fuel entry data.

**Content-Type:** `application/json`


_Schema: array of _

### Responses

#### `200` — Successful operation.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-fuel-response` |  | This array returns the list of fuel entries. |  |
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

## PUT `/mifleet/fuel/{id}`

**Update MiFleet Fuel Entry**

Updates specific MiFleet fuel entries. This API endpoint facilitates modifications to fuel transaction information, including updates to document details, vehicle information, and validation status.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs. It is essential for maintaining accurate and current fuel transaction records.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the fuel entry to be updated. | 1010 |

### Request Body

JSON payload containing the fuel entry data.

**Content-Type:** `application/json`


_Schema: _

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | `mifleet-fuel-response` |  |  |  |
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

## DELETE `/mifleet/fuel/{id}`

**Delete MiFleet Fuel Entries**

Deletes a specific fuel entry from the MiFleet system.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the fuel entry to be deleted. | 1010 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

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

## GET `/mifleet/maintenance`

**Get MiFleet Maintenance Entries**

Retrieves a detailed list of maintenance entries for MiFleet. This endpoint is designed for comprehensive tracking and management of maintenance-related expenses and activities.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[date_from]` | optional | `date` | Start date and time for filtering breakdown entries. Only entries recorded on or after this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[date_to]` | optional | `date` | End date and time for filtering breakdown entries. Only entries recorded on or before this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `filter[id]` | optional | integer | Optional filter to retrieve by identification number. | 67890 |
| `filter[ids]` | optional | string | Filter by multiple entry IDs, separated by commas. Suitable for batch queries. | 67890,67891 |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-maintenance-response` |  | This array returns the list of maintenance entries. |  |
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

## POST `/mifleet/maintenance`

**Create a New MiFleet Maintenance Entry**

Creates new maintenance entries in the MiFleet system. This endpoint is used to add detailed records of maintenance activities, including service types, costs, and associated vehicle information. It supports the submission of multiple entries at once for efficient batch processing.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Request Body

JSON payload containing the details of maintenance entries. The structure should follow the schema defined in "create_mifleet_maintenance". Each entry includes comprehensive maintenance data, which is critical for accurate record-keeping and analysis.

**Content-Type:** `application/json`


_Schema: array of _

### Responses

#### `200` — Successful operation.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-maintenance-response` |  | This array returns the list of maintenance entries. |  |
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

## PUT `/mifleet/maintenance/{id}`

**Update MiFleet Maintenance Entry**

Updates an existing maintenance entry in the MiFleet system. This endpoint allows for modifications to various aspects of a maintenance record, including service details, costs, and vehicle information. It is vital for keeping maintenance records up-to-date and accurate.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the maintenance entry to be updated. | 1010 |

### Request Body

JSON payload containing the details of maintenance entries. The structure should follow the schema defined in "update_mifleet_maintenance". Each entry includes comprehensive maintenance data, which is critical for accurate record-keeping and analysis.

**Content-Type:** `application/json`


_Schema: _

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | `mifleet-maintenance-response` |  |  |  |
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

## DELETE `/mifleet/maintenance/{id}`

**Delete MiFleet Maintenance Entries**

Deletes a specific maintenance entry from the MiFleet system.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the maintenance entry to be deleted. | 1010 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

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

## GET `/mifleet/toll`

**Get MiFleet Toll Entries**

Retrieves a detailed list of toll entries for MiFleet. This endpoint is designed for comprehensive tracking and management of toll-related expenses and activities.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[date_from]` | optional | `date` | Start date and time for filtering breakdown entries. Only entries recorded on or after this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[date_to]` | optional | `date` | End date and time for filtering breakdown entries. Only entries recorded on or before this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `filter[id]` | optional | integer | Optional filter to retrieve by identification number. | 67890 |
| `filter[ids]` | optional | string | Filter by multiple entry IDs, separated by commas. Suitable for batch queries. | 67890,67891 |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-toll-response` |  | This array returns the list of toll entries. |  |
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

## POST `/mifleet/toll`

**Create a New MiFleet Toll Validation Entry**

Creates new toll validation entries in the MiFleet system. This endpoint is used for adding toll transaction records, including details like toll amounts, vehicle information, and transaction dates. It supports batch processing, allowing multiple entries to be submitted simultaneously.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Request Body

JSON payload containing the details of toll validation entries. Each entry within the array should adhere to the structure defined in "create_mifleet_toll", encompassing all relevant toll transaction data.

**Content-Type:** `application/json`


_Schema: array of _

### Responses

#### `200` — Successful operation.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-toll-response` |  | This array returns the list of toll entries. |  |
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

## PUT `/mifleet/toll/{id}`

**Update MiFleet Toll Validation Entry**

Updates an existing MiFleet toll validation entry. This endpoint enables modifications to toll transaction records, allowing changes to be made to details such as toll amounts, vehicle data, and dates. It is crucial for ensuring the accuracy and currency of toll records.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the toll entry to be updated. | 1010 |

### Request Body

JSON payload containing the details of toll validation entries. Each entry within the array should adhere to the structure defined in "create_mifleet_toll", encompassing all relevant toll transaction data.

**Content-Type:** `application/json`


_Schema: _

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | `mifleet-toll-response` |  |  |  |
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

## DELETE `/mifleet/toll/{id}`

**Delete MiFleet Toll Entries**

Deletes a specific toll entry from the MiFleet system.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the toll entry to be deleted. | 1010 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

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

## GET `/mifleet/accident`

**Get MiFleet Accident Entries**

Retrieves a detailed list of accident entries for MiFleet. This endpoint is designed for comprehensive tracking and management of accident-related expenses and activities.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[date_from]` | optional | `date` | Start date and time for filtering breakdown entries. Only entries recorded on or after this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[date_to]` | optional | `date` | End date and time for filtering breakdown entries. Only entries recorded on or before this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `filter[id]` | optional | integer | Optional filter to retrieve by identification number. | 67890 |
| `filter[ids]` | optional | string | Filter by multiple entry IDs, separated by commas. Suitable for batch queries. | 67890,67891 |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-accident-response` |  | This array returns the list of accident entries. |  |
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

## POST `/mifleet/accident`

**Create New MiFleet Accident Entries**

Creates a new accident entry in the MiFleet system. This endpoint allows for adding accident entries to facilitate tracking and managing accident costs.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Request Body

JSON payload containing the transaction entry data.

**Content-Type:** `application/json`


_Schema: array of _

### Responses

#### `200` — Successful operation.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-accident-response` |  | This array returns the list of accident entries. |  |
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

## PUT `/mifleet/accident/{id}`

**Update MiFleet Accident Transaction Entry**

Updates an existing MiFleet accident entry. This endpoint allows for modifying specific details of an accident entry identified by its unique ID.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the accident entry to be updated. | 1010 |

### Request Body

JSON payload containing the transaction entry data.

**Content-Type:** `application/json`


_Schema: _

### Responses

#### `200` — Successful operation.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | `mifleet-accident-response` |  |  |  |
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

## DELETE `/mifleet/accident/{id}`

**Delete MiFleet Accident Entries**

Deletes a specific accident entry from the MiFleet system.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the accident entry to be deleted. | 1010 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

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

## GET `/mifleet/breakdown`

**Get MiFleet Breakdown Entries**

Retrieves a detailed list of breakdown entries for MiFleet. This endpoint is designed for comprehensive tracking and management of breakdown-related expenses and activities.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[date_from]` | optional | `date` | Start date and time for filtering breakdown entries. Only entries recorded on or after this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[date_to]` | optional | `date` | End date and time for filtering breakdown entries. Only entries recorded on or before this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `filter[id]` | optional | integer | Optional filter to retrieve by identification number. | 67890 |
| `filter[ids]` | optional | string | Filter by multiple entry IDs, separated by commas. Suitable for batch queries. | 67890,67891 |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-breakdown-response` |  | This array returns the list of breakdown entries. |  |
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

## POST `/mifleet/breakdown`

**Create New MiFleet Breakdown Entry**

Creates a new breakdown entry in the MiFleet system. This endpoint allows for adding breakdown entries to facilitate tracking and managing breakdowns.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Request Body

JSON payload containing the breakdown entry data.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of  |  | Array of breakdown entries to be created. Each entry should conform to the MiFleet breakdown structure defined in the schema. |  |

### Responses

#### `200` — Successful operation.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-breakdown-response` |  | This array returns the list of breakdown entries. |  |
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

## PUT `/mifleet/breakdown/{id}`

**Update MiFleet Breakdown Validation Entry**

Updates an existing MiFleet breakdown entry. This endpoint allows for modifying specific details of a breakdown entry identified by its unique ID.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the breakdown entry to be updated. | 1010 |

### Request Body

JSON payload containing the breakdown entry data.

**Content-Type:** `application/json`


_Schema: _

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | `mifleet-breakdown-response` |  |  |  |
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

## DELETE `/mifleet/breakdown/{id}`

**Delete MiFleet Breakdown Entries**

Deletes a specific breakdown entry from the MiFleet system.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the breakdown entry to be deleted. | 1010 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

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

## GET `/mifleet/cleaning`

**Get MiFleet Cleaning Entries**

Retrieves a detailed list of cleaning entries for MiFleet. This endpoint is designed for comprehensive tracking and management of cleaning-related expenses and activities.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[date_from]` | optional | `date` | Start date and time for filtering breakdown entries. Only entries recorded on or after this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[date_to]` | optional | `date` | End date and time for filtering breakdown entries. Only entries recorded on or before this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `filter[id]` | optional | integer | Optional filter to retrieve by identification number. | 67890 |
| `filter[ids]` | optional | string | Filter by multiple entry IDs, separated by commas. Suitable for batch queries. | 67890,67891 |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-cleaning-response` |  | This array returns the list of cleaning entries. |  |
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

## POST `/mifleet/cleaning`

**Create New MiFleet cleaning Entry**

Creates a new cleaning entry in the MiFleet system. This endpoint allows for adding cleaning entries to facilitate tracking and managing cleaning.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Request Body

JSON payload containing the cleaning entry data.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of  |  | Array of cleaning entries to be created. Each entry should conform to the MiFleet cleaning structure defined in the schema. |  |

### Responses

#### `200` — Successful operation.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-cleaning-response` |  | This array returns the list of cleaning entries. |  |
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

## PUT `/mifleet/cleaning/{id}`

**Update MiFleet Cleaning Entry**

Updates an existing MiFleet cleaning entry. This endpoint allows for modifying specific details of a cleaning entry identified by its unique ID.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the cleaning entry to be updated. | 1010 |

### Request Body

JSON payload containing the cleaning entry data.

**Content-Type:** `application/json`


_Schema: _

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | `mifleet-cleaning-response` |  |  |  |
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

## DELETE `/mifleet/cleaning/{id}`

**Delete MiFleet Cleaning Entries**

Deletes a specific cleaning entry from the MiFleet system.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the cleaning entry to be deleted. | 1010 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

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

## GET `/mifleet/consumable`

**Get MiFleet Consumable Entries**

Retrieves a detailed list of consumable entries for MiFleet. This endpoint is designed for comprehensive tracking and management of consumable-related expenses and activities.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[date_from]` | optional | `date` | Start date and time for filtering breakdown entries. Only entries recorded on or after this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[date_to]` | optional | `date` | End date and time for filtering breakdown entries. Only entries recorded on or before this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `filter[id]` | optional | integer | Optional filter to retrieve by identification number. | 67890 |
| `filter[ids]` | optional | string | Filter by multiple entry IDs, separated by commas. Suitable for batch queries. | 67890,67891 |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-consumable-response` |  | This array returns the list of consumable entries. |  |
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

## POST `/mifleet/consumable`

**Create New MiFleet Consumable Entries**

Creates a new consumable entry in the MiFleet system. This endpoint allows for adding consumable entries to facilitate tracking and managing consumable costs.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Request Body

JSON payload containing the consumable entry data.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of  |  | Array of consumable entries to be created. Each entry should conform to the MiFleet consumable structure defined in the schema. |  |

### Responses

#### `200` — Successful operation.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-consumable-response` |  | This array returns the list of consumable entries. |  |
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

## PUT `/mifleet/consumable/{id}`

**Update MiFleet Consumable Transaction Entry**

Updates an existing MiFleet consumable entry. This endpoint allows for modifying specific details of an consumable entry identified by its unique ID.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the consumable entry to be updated. | 1010 |

### Request Body

JSON payload containing the consumable entry data.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of  |  | Array of consumable entries to be created. Each entry should conform to the MiFleet consumable structure defined in the schema. |  |

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | `mifleet-consumable-response` |  |  |  |
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

## DELETE `/mifleet/consumable/{id}`

**Delete MiFleet Consumable Entries**

Deletes a specific consumable entry from the MiFleet system.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the consumable entry to be deleted. | 1010 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

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

## GET `/mifleet/driver-cost`

**Get MiFleet Driver Cost Entries**

Retrieves a detailed list of driver cost entries for MiFleet. This endpoint is designed for comprehensive tracking and management of driver-related expenses and activities.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[date_from]` | optional | `date` | Start date and time for filtering breakdown entries. Only entries recorded on or after this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[date_to]` | optional | `date` | End date and time for filtering breakdown entries. Only entries recorded on or before this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[id]` | optional | integer | Optional filter to retrieve by identification number. | 67890 |
| `filter[ids]` | optional | string | Filter by multiple entry IDs, separated by commas. Suitable for batch queries. | 67890,67891 |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-driver-cost-response` |  | This array returns the list of driver cost entries. |  |
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

## POST `/mifleet/driver-cost`

**Create New MiFleet Driver Cost Entries**

Creates a new driver cost entry in the MiFleet system. This endpoint allows for adding driver cost entries to facilitate tracking and managing driver costs.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Request Body

JSON payload containing the driver cost entry data.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of  |  | Array of driver cost entries to be created. Each entry should conform to the MiFleet driver cost structure defined in the schema. |  |

### Responses

#### `200` — Successful operation.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-driver-cost-response` |  | This array returns the list of driver cost entries. |  |
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

## PUT `/mifleet/driver-cost/{id}`

**Update MiFleet Driver Cost Transaction Entry**

Updates an existing MiFleet driver cost entry. This endpoint allows for modifying specific details of a driver cost entry identified by its unique ID.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the driver cost entry to be updated. | 1010 |

### Request Body

JSON payload containing the driver cost entry data.

**Content-Type:** `application/json`


_Schema: _

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | `mifleet-driver-cost-response` |  |  |  |
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

## DELETE `/mifleet/driver-cost/{id}`

**Delete MiFleet Driver Cost Entries**

Deletes a specific driver cost entry from the MiFleet system.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the driver cost entry to be deleted. | 1010 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

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

## GET `/mifleet/driver-license`

**Get MiFleet Driver License Entries**

Retrieves a detailed list of driver license entries for MiFleet. This endpoint is designed for comprehensive tracking and management of driver license-related expenses and activities.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[date_from]` | optional | `date` | Start date and time for filtering breakdown entries. Only entries recorded on or after this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[date_to]` | optional | `date` | End date and time for filtering breakdown entries. Only entries recorded on or before this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[id]` | optional | integer | Optional filter to retrieve by identification number. | 67890 |
| `filter[ids]` | optional | string | Filter by multiple entry IDs, separated by commas. Suitable for batch queries. | 67890,67891 |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-driver-license-response` |  | This array returns the list of driver license entries. |  |
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

## POST `/mifleet/driver-license`

**Create New MiFleet Driver License Entries**

Creates a new driver license entry in the MiFleet system. This endpoint allows for adding driver license entries to facilitate tracking and managing driver license costs.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Request Body

JSON payload containing the driver license entry data.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of  |  | Array of driver license entries to be created. Each entry should conform to the MiFleet driver license structure defined in the schema. |  |

### Responses

#### `200` — Successful operation.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-driver-license-response` |  | This array returns the list of driver license entries. |  |
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

## PUT `/mifleet/driver-license/{id}`

**Update MiFleet Driver License Transaction Entry**

Updates an existing MiFleet driver license entry. This endpoint allows for modifying specific details of a driver license entry identified by its unique ID.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the driver license entry to be updated. | 1010 |

### Request Body

JSON payload containing the driver license entry data.

**Content-Type:** `application/json`


_Schema: _

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | `mifleet-driver-license-response` |  |  |  |
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

## DELETE `/mifleet/driver-license/{id}`

**Delete MiFleet Driver License Entries**

Deletes a specific driver license entry from the MiFleet system.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the driver license entry to be deleted. | 1010 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

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

## GET `/mifleet/contract/financing`

**Get MiFleet Contracts for Financing**

Retrieves a detailed list of financing contracts for MiFleet. This endpoint is designed for comprehensive tracking and management of financing-related contracts.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[date_from]` | optional | `date` | Start date and time for filtering breakdown entries. Only entries recorded on or after this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[date_to]` | optional | `date` | End date and time for filtering breakdown entries. Only entries recorded on or before this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `filter[id]` | optional | integer | Optional filter to retrieve by identification number. | 67890 |
| `filter[ids]` | optional | string | Filter by multiple contract IDs, separated by commas. Suitable for batch queries. | 67890,67891 |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-contract-financing-response` |  | This array returns the list of contract financing entries. |  |
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

## POST `/mifleet/contract/financing`

**Create New MiFleet Financing Contract**

Creates a new financing contract in the MiFleet system. This endpoint allows for adding financing contracts to facilitate tracking and managing financing contracts.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Request Body

JSON payload containing the driver license entry data.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of  |  | JSON payload containing the transaction entry data. |  |

### Responses

#### `200` — Successful operation.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-contract-financing-response` |  | This array returns the list of contract financing entries. |  |
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

## PUT `/mifleet/contract/financing/{id}`

**Update MiFleet Financing Contract**

Updates an existing MiFleet financing contract. This endpoint allows for modifying specific details of a financing contract identified by its unique ID.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the financing contract to be updated. | 1010 |

### Request Body

JSON payload containing the driver license entry data.

**Content-Type:** `application/json`


_Schema: _

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | `mifleet-contract-financing-response` |  |  |  |
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

## DELETE `/mifleet/contract/financing/{id}`

**Delete MiFleet Financing Contracts**

Deletes a specific financing contract from the MiFleet system.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the financing contract to be deleted. | 1010 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

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

## GET `/mifleet/financing`

**Get MiFleet Financing Entries**

Retrieves a detailed list of financing entries for MiFleet. This endpoint is designed for comprehensive tracking and management of financing-related expenses and activities.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[date_from]` | optional | `date` | Start date and time for filtering breakdown entries. Only entries recorded on or after this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[date_to]` | optional | `date` | End date and time for filtering breakdown entries. Only entries recorded on or before this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `filter[id]` | optional | integer | Optional filter to retrieve by identification number. | 67890 |
| `filter[ids]` | optional | string | Filter by multiple entry IDs, separated by commas. Suitable for batch queries. | 67890,67891 |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-financing-response` |  | This array returns the list of financing transaction entries. |  |
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

## POST `/mifleet/financing`

**Create New MiFleet Financing Entries**

Creates a new financing entry in the MiFleet system. This endpoint allows for adding financing entries to facilitate tracking and managing financing costs.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Request Body

JSON payload containing the transaction entry data.

**Content-Type:** `application/json`


_Schema: array of _

### Responses

#### `200` — Successful operation.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-financing-response` |  | This array returns the list of financing entries. |  |
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

## PUT `/mifleet/financing/{id}`

**Update MiFleet Financing Transaction Entry**

Updates an existing MiFleet financing entry. This endpoint allows for modifying specific details of a financing entry identified by its unique ID.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the financing entry to be updated. | 1010 |

### Request Body

JSON payload containing the transaction entry data.

**Content-Type:** `application/json`


_Schema: _

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | `mifleet-financing-response` |  |  |  |
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

## DELETE `/mifleet/financing/{id}`

**Delete MiFleet Financing Entries**

Deletes a specific financing entry from the MiFleet system.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the financing entry to be deleted. | 1010 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

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

## GET `/mifleet/fine`

**Get MiFleet Fine Entries**

Retrieves a detailed list of fine entries for MiFleet. This endpoint is designed for comprehensive tracking and management of fine-related expenses and activities.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[date_from]` | optional | `date` | Start date and time for filtering breakdown entries. Only entries recorded on or after this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[date_to]` | optional | `date` | End date and time for filtering breakdown entries. Only entries recorded on or before this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `filter[id]` | optional | integer | Optional filter to retrieve by identification number. | 67890 |
| `filter[ids]` | optional | string | Filter by multiple entry IDs, separated by commas. Suitable for batch queries. | 67890,67891 |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-fine-response` |  | This array returns the list of fine entries. |  |
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

## POST `/mifleet/fine`

**Create New MiFleet Fine Validation Entry**

Creates a new fine validation entry in the MiFleet system. This endpoint allows for adding fine entries to facilitate tracking and managing fines.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Request Body

JSON payload containing the fine entry data.

**Content-Type:** `application/json`


_Schema: array of _

### Responses

#### `200` — Successful operation.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-fine-response` |  | This array returns the list of fine entries. |  |
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

## PUT `/mifleet/fine/{id}`

**Update MiFleet Fine Validation Entry**

Updates an existing MiFleet fine validation entry. This endpoint allows for modifying specific details of a fine entry identified by its unique ID.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the fine entry to be updated. | 1010 |

### Request Body

JSON payload containing the fine entry data.

**Content-Type:** `application/json`


_Schema: _

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | `mifleet-fine-response` |  |  |  |
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

## DELETE `/mifleet/fine/{id}`

**Delete MiFleet Fine Entries**

Deletes a specific fine entry from the MiFleet system.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the fine entry to be deleted. | 1010 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

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

## GET `/mifleet/contract/fuel-card`

**Get MiFleet Contracts for Fuel Card**

Retrieves a detailed list of fuel card contracts for MiFleet. This endpoint is designed for comprehensive tracking and management of fuel card-related contracts.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[date_from]` | optional | `date` | Start date and time for filtering breakdown entries. Only entries recorded on or after this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[date_to]` | optional | `date` | End date and time for filtering breakdown entries. Only entries recorded on or before this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `filter[id]` | optional | integer | Optional filter to retrieve by identification number. | 67890 |
| `filter[ids]` | optional | string | Filter by multiple contract IDs, separated by commas. Suitable for batch queries. | 67890,67891 |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-contract-fuel-card-response` |  | This array returns the list of fuel card contracts. |  |
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

## POST `/mifleet/contract/fuel-card`

**Create New MiFleet Fuel Card Contract**

Creates a new fuel card contract in the MiFleet system. This endpoint allows for adding fuel card contracts to facilitate tracking and managing fuel card contracts.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Request Body

JSON payload containing the fine entry data.

**Content-Type:** `application/json`


_Schema: array of _

### Responses

#### `200` — Successful operation.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-contract-fuel-card-response` |  | This array returns the list of fuel card contracts. |  |
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

## PUT `/mifleet/contract/fuel-card/{id}`

**Update MiFleet Fuel Card Contract**

Updates an existing MiFleet fuel card contract. This endpoint allows for modifying specific details of a fuel card contract identified by its unique ID.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the fuel card contract to be updated. | 1010 |

### Request Body

JSON payload containing the fine entry data.

**Content-Type:** `application/json`


_Schema: _

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | `mifleet-contract-fuel-card-response` |  |  |  |
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

## DELETE `/mifleet/contract/fuel-card/{id}`

**Delete MiFleet Fuel Card Contracts**

Deletes a specific fuel card contract from the MiFleet system.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the fuel card contract to be deleted. | 1010 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

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

## GET `/mifleet/contract/insurance`

**Get MiFleet Contracts for Insurance**

Retrieves a detailed list of insurance contracts for MiFleet. This endpoint is designed for comprehensive tracking and management of insurance-related contracts.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[date_from]` | optional | `date` | Start date and time for filtering breakdown entries. Only entries recorded on or after this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[date_to]` | optional | `date` | End date and time for filtering breakdown entries. Only entries recorded on or before this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `filter[id]` | optional | integer | Optional filter to retrieve by identification number. | 67890 |
| `filter[ids]` | optional | string | Filter by multiple contract IDs, separated by commas. Suitable for batch queries. | 67890,67891 |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-contract-insurance-response` |  | This array returns the list of created insurance contracts. |  |
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

## POST `/mifleet/contract/insurance`

**Create New MiFleet Insurance Contract**

Creates a new insurance contract in the MiFleet system. This endpoint allows for adding insurance contracts to facilitate tracking and managing insurance contracts.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Request Body

JSON payload containing the fine entry data.

**Content-Type:** `application/json`


_Schema: array of `mifleet-contract-insurance`_

### Responses

#### `200` — Successful operation.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-contract-insurance-response` |  | This array returns the list of created insurance contracts. |  |
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

## PUT `/mifleet/contract/insurance/{id}`

**Update MiFleet Insurance Contract**

Updates an existing MiFleet insurance contract. This endpoint allows for modifying specific details of a insurance contract identified by its unique ID.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the insurance contract to be updated. | 1010 |

### Request Body

JSON payload containing the fine entry data.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `contract_status` | string |  | Current status of the contract, e.g., created, active, or concluded. | "CONTRACT_STATUS_ACTIVE" |
| `contract_date` | `date` |  | The date of the contract. |  |
| `contract_start_date` | `date` |  | The date the contract is effective. |  |
| `contract_end_date` | `date` |  | The date of conclusion of the contract. |  |
| `notes` | string |  | Notes on the contract, detailing the nature of products/services. | "This is a sample note for the fuel card contract." |
| `registration` | `registration` |  |  |  |
| `odometer` | integer |  | Odometer reading at the time of contract initiation. | 15000 |
| `supplier` | string |  | Supplier or service provider for the entry. | "ABC Towing" |
| `net_value` | number |  | Net value before tax. | 100 |
| `tax_rate` | number |  | Tax rate applied. | 0.15 |
| `total_value` | number |  | Total value including tax. | 115 |
| `general_ledger_code` | integer |  | Code used for categorizing this transaction in the general ledger. | 12345 |
| `payment_term` | string |  | The term (in days) of payments for the contract. Importantly, an integer needs to be present in the string as per example. | "NET_30" |
| `payment_method` | string |  | The method of payment associated to the contract. | "CREDIT_CARD" |
| `insurance_type` | string |  | The type of insurance associated with the contract. | "FULL_COVERAGE" |
| `policy_number` | string |  | The insurance policy number for the contract. | "INS-123456789" |
| `franchise_percentage` | number |  | Any value assigned to the franchise as a percentage. | 10 |
| `franchise_value` | number |  | Any value assigned to the franchise as a flat value. | 500 |
| `insurance_conditions` | array of object |  | An optional array of conditions involved with the contract. |  |

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | `mifleet-contract-insurance-response` |  |  |  |
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

## DELETE `/mifleet/contract/insurance/{id}`

**Delete MiFleet Insurance Contracts**

Deletes a specific insurance contract from the MiFleet system.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the insurance contract to be deleted. | 1010 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

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

## GET `/mifleet/insurance`

**Get MiFleet Insurance Entries**

Retrieves a detailed list of insurance entries for MiFleet. This endpoint is designed for comprehensive tracking and management of insurance-related expenses and activities.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[date_from]` | optional | `date` | Start date and time for filtering breakdown entries. Only entries recorded on or after this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[date_to]` | optional | `date` | End date and time for filtering breakdown entries. Only entries recorded on or before this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `filter[id]` | optional | integer | Optional filter to retrieve by identification number. | 67890 |
| `filter[ids]` | optional | string | Filter by multiple entry IDs, separated by commas. Suitable for batch queries. | 67890,67891 |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-insurance-response` |  | This array returns the list of created insurance entries. |  |
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

## POST `/mifleet/insurance`

**Create New MiFleet Insurance Entries**

Creates a new insurance entry in the MiFleet system. This endpoint allows for adding insurance entries to facilitate tracking and managing insurance costs.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Request Body

JSON payload containing the fine entry data.

**Content-Type:** `application/json`


_Schema: array of _

### Responses

#### `200` — Successful operation.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-insurance-response` |  | This array returns the list of created insurance entries. |  |
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

## PUT `/mifleet/insurance/{id}`

**Update MiFleet Insurance Transaction Entry**

Updates an existing MiFleet insurance entry. This endpoint allows for modifying specific details of a insurance entry identified by its unique ID.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the insurance entry to be updated. | 1010 |

### Request Body

JSON payload containing the fine entry data.

**Content-Type:** `application/json`


_Schema: _

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | `mifleet-insurance-response` |  |  |  |
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

## DELETE `/mifleet/insurance/{id}`

**Delete MiFleet Insurance Entries**

Deletes a specific insurance entry from the MiFleet system.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the insurance entry to be deleted. | 1010 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

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

## GET `/mifleet/leasing-cost`

**Get MiFleet Leasing Cost Entries**

Retrieves a detailed list of leasing cost entries for MiFleet. This endpoint is designed for comprehensive tracking and management of leasing cost-related expenses and activities.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[date_from]` | optional | `date` | Start date and time for filtering breakdown entries. Only entries recorded on or after this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[date_to]` | optional | `date` | End date and time for filtering breakdown entries. Only entries recorded on or before this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `filter[id]` | optional | integer | Optional filter to retrieve by identification number. | 67890 |
| `filter[ids]` | optional | string | Filter by multiple entry IDs, separated by commas. Suitable for batch queries. | 67890,67891 |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-leasing-cost-response` |  | This array returns the list of leasing cost entries. |  |
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

## POST `/mifleet/leasing-cost`

**Create New MiFleet Leasing Cost Entries**

Creates a new leasing cost entry in the MiFleet system. This endpoint allows for adding leasing cost entries to facilitate tracking and managing leasing costs.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Request Body

**Content-Type:** `application/json`


_Schema: array of _

### Responses

#### `200` — Successful operation.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-leasing-cost-response` |  | This array returns the list of leasing cost entries. |  |
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

## PUT `/mifleet/leasing-cost/{id}`

**Update MiFleet Leasing Cost Transaction Entry**

Updates an existing MiFleet leasing cost entry. This endpoint allows for modifying specific details of a leasing cost entry identified by its unique ID.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the leasing cost entry to be updated. | 1010 |

### Request Body

**Content-Type:** `application/json`


_Schema: _

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | `mifleet-leasing-cost-response` |  |  |  |
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

## DELETE `/mifleet/leasing-cost/{id}`

**Delete MiFleet Leasing Cost Entries**

Deletes a specific leasing cost entry from the MiFleet system.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the leasing cost entry to be deleted. | 1010 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

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

## GET `/mifleet/contract/maintenance`

**Get MiFleet Contracts for Maintenance**

Retrieves a detailed list of maintenance contracts for MiFleet. This endpoint is designed for comprehensive tracking and management of maintenance-related contracts.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[date_from]` | optional | `date` | Start date and time for filtering breakdown entries. Only entries recorded on or after this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[date_to]` | optional | `date` | End date and time for filtering breakdown entries. Only entries recorded on or before this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `filter[id]` | optional | integer | Optional filter to retrieve by identification number. | 67890 |
| `filter[ids]` | optional | string | Filter by multiple contract IDs, separated by commas. Suitable for batch queries. | 67890,67891 |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  | This array returns the list of contract maintenance entries. |  |
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

## POST `/mifleet/contract/maintenance`

**Create New MiFleet Contract Maintenance Entries**

Creates new contract maintenance entries in the MiFleet system. This endpoint allows for adding maintenance contract entries to facilitate tracking and managing maintenance contracts.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Request Body

**Content-Type:** `application/json`


_Schema: array of object_

### Responses

#### `200` — Successful operation.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  | This array returns the list of contract maintenance entries. |  |
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

## PUT `/mifleet/contract/maintenance/{id}`

**Update MiFleet Maintenance Contract**

Updates an existing MiFleet maintenance contract. This endpoint allows for modifying specific details of a maintenance contract identified by its unique ID.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the maintenance contract to be updated. | 1010 |

### Request Body

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `contract_status` | string | null |  | Current status of the contract, e.g., created, active, or concluded. | "CONTRACT_STATUS_ACTIVE" |
| `contract_date` | `date` |  | The date of the contract. |  |
| `contract_start_date` | `date` |  | The date the contract is effective. |  |
| `contract_end_date` | `date` |  | The date of conclusion of the contract. |  |
| `notes` | string | null |  | Notes on the contract, detailing the nature of products/services. | "Monthly rental maintenance contract for trailer." |
| `registration` | `registration` |  |  |  |
| `odometer` | integer | null |  | Any applicable odometer value that pertains to the contract. | 101000 |
| `supplier` | string |  | Name of the supplier or entity providing value or service. | "ABC Bank" |
| `net_value` | number |  | Net value of the contract, calculated before taxes. | 4.45 |
| `tax_rate` | number |  | Applicable tax rate for the contract, represented as a decimal fraction. | 0.2 |
| `total_value` | number |  | Total value of the contract after applying any taxes. | 4.99 |
| `general_ledger_code` | integer | null |  | Code used for categorizing this transaction in the general ledger. | 54321 |
| `payment_term` | string | null |  | The term (in days) of payments for the contract. Importantly, an integer needs to be present in the string as per example. | "30 Days" |
| `payment_method` | string | null |  | The method of payment associated to the contract. | "Direct Debit" |
| `maintenance_type` | string |  | The type of maintenance associated with the contract. | "Service B" |
| `warranty_date` | `date` |  | The date of warranty cover for the contract. |  |
| `warranty_odometer` | integer | null |  | The odometer of warranty cover for the contract. | 111000 |
| `service_interval_months` | integer | null |  | The monthly interval for services covered under the contract. | 12 |
| `service_interval_odometer` | integer | null |  | The odometer interval for services covered under the contract. | 20000 |

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  |  |  |
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

## DELETE `/mifleet/contract/maintenance/{id}`

**Delete MiFleet Maintenance Contracts**

Deletes a specific maintenance contract from the MiFleet system.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the maintenance contract to be deleted. | 1010 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

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

## GET `/mifleet/oil`

**Get MiFleet Oil Entries**

Retrieves a detailed list of oil entries for MiFleet. This endpoint is designed for comprehensive tracking and management of oil-related expenses and activities.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[date_from]` | optional | `date` | Start date and time for filtering breakdown entries. Only entries recorded on or after this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[date_to]` | optional | `date` | End date and time for filtering breakdown entries. Only entries recorded on or before this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `filter[id]` | optional | integer | Optional filter to retrieve by identification number. | 67890 |
| `filter[ids]` | optional | string | Filter by multiple entry IDs, separated by commas. Suitable for batch queries. | 67890,67891 |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-oil-response` |  | This array returns the list of oil entries. |  |
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

## POST `/mifleet/oil`

**Create New MiFleet Oil Entries**

Creates a new oil entry in the MiFleet system. This endpoint allows for adding oil entries to facilitate tracking and managing oil costs.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Request Body

**Content-Type:** `application/json`


_Schema: array of _

### Responses

#### `200` — Successful operation.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-oil-response` |  | This array returns the list of oil entries. |  |
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

## PUT `/mifleet/oil/{id}`

**Update MiFleet Oil Transaction Entry**

Updates an existing MiFleet oil entry. This endpoint allows for modifying specific details of a oil entry identified by its unique ID.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the oil entry to be updated. | 1010 |

### Request Body

**Content-Type:** `application/json`


_Schema: _

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | `mifleet-oil-response` |  |  |  |
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

## DELETE `/mifleet/oil/{id}`

**Delete MiFleet Oil Entries**

Deletes a specific oil entry from the MiFleet system.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the oil entry to be deleted. | 1010 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

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

## GET `/mifleet/purchase`

**Get MiFleet Purchase Entries**

Retrieves a detailed list of purchase entries for MiFleet. This endpoint is designed for comprehensive tracking and management of purchase-related expenses and activities.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[date_from]` | optional | `date` | Start date and time for filtering breakdown entries. Only entries recorded on or after this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[date_to]` | optional | `date` | End date and time for filtering breakdown entries. Only entries recorded on or before this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `filter[id]` | optional | integer | Optional filter to retrieve by identification number. | 67890 |
| `filter[ids]` | optional | string | Filter by multiple entry IDs, separated by commas. Suitable for batch queries. | 67890,67891 |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-purchase-response` |  | This array returns the list of purchase entries. |  |
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

## POST `/mifleet/purchase`

**Create New MiFleet Purchase Entries**

Creates a new purchase entry in the MiFleet system. This endpoint allows for adding purchase entries to facilitate tracking and managing purchase costs.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Request Body

**Content-Type:** `application/json`


_Schema: array of _

### Responses

#### `200` — Successful operation.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-purchase-response` |  | This array returns the list of purchase entries. |  |
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

## PUT `/mifleet/purchase/{id}`

**Update MiFleet Purchase Transaction Entry**

Updates an existing MiFleet purchase entry. This endpoint allows for modifying specific details of a purchase entry identified by its unique ID.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the purchase entry to be updated. | 1010 |

### Request Body

**Content-Type:** `application/json`


_Schema: _

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | `mifleet-purchase-response` |  |  |  |
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

## DELETE `/mifleet/purchase/{id}`

**Delete MiFleet Purchase Entries**

Deletes a specific purchase entry from the MiFleet system.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the purchase entry to be deleted. | 1010 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

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

## GET `/mifleet/rental-cost`

**Get MiFleet Rental Cost Entries**

Retrieves a detailed list of rental cost entries for MiFleet. This endpoint is designed for comprehensive tracking and management of rental cost-related expenses and activities.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[date_from]` | optional | `date` | Start date and time for filtering breakdown entries. Only entries recorded on or after this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[date_to]` | optional | `date` | End date and time for filtering breakdown entries. Only entries recorded on or before this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `filter[id]` | optional | integer | Optional filter to retrieve by identification number. | 67890 |
| `filter[ids]` | optional | string | Filter by multiple entry IDs, separated by commas. Suitable for batch queries. | 67890,67891 |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-rental-cost-response` |  | This array returns the list of rental cost entries. |  |
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

## POST `/mifleet/rental-cost`

**Create New MiFleet Rental Cost Entries**

Creates a new rental cost entry in the MiFleet system. This endpoint allows for adding rental cost entries to facilitate tracking and managing rental cost costs.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Request Body

**Content-Type:** `application/json`


_Schema: array of _

### Responses

#### `200` — Successful operation.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-rental-cost-response` |  | This array returns the list of rental cost entries. |  |
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

## PUT `/mifleet/rental-cost/{id}`

**Update MiFleet Rental Cost Transaction Entry**

Updates an existing MiFleet rental cost entry. This endpoint allows for modifying specific details of a rental cost entry identified by its unique ID.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the rental cost entry to be updated. | 1010 |

### Request Body

**Content-Type:** `application/json`


_Schema: _

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | `mifleet-rental-cost-response` |  |  |  |
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

## DELETE `/mifleet/rental-cost/{id}`

**Delete MiFleet Rental Cost Entries**

Deletes a specific rental cost entry from the MiFleet system.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the rental cost entry to be deleted. | 1010 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

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

## GET `/mifleet/tax`

**Get MiFleet Tax Entries**

Retrieves a detailed list of tax entries for MiFleet. This endpoint is designed for comprehensive tracking and management of tax-related expenses and activities.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[date_from]` | optional | `date` | Start date and time for filtering breakdown entries. Only entries recorded on or after this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[date_to]` | optional | `date` | End date and time for filtering breakdown entries. Only entries recorded on or before this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `filter[id]` | optional | integer | Optional filter to retrieve by identification number. | 67890 |
| `filter[ids]` | optional | string | Filter by multiple entry IDs, separated by commas. Suitable for batch queries. | 67890,67891 |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-tax-response` |  | This array returns the list of tax entries. |  |
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

## POST `/mifleet/tax`

**Create New MiFleet Tax Entries**

Creates a new tax entry in the MiFleet system. This endpoint allows for adding tax entries to facilitate tracking and managing tax costs.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Request Body

**Content-Type:** `application/json`


_Schema: array of _

### Responses

#### `200` — Successful operation.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-tax-response` |  | This array returns the list of tax entries. |  |
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

## PUT `/mifleet/tax/{id}`

**Update MiFleet Tax Transaction Entry**

Updates an existing MiFleet tax entry. This endpoint allows for modifying specific details of a tax entry identified by its unique ID.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the tax entry to be updated. | 1010 |

### Request Body

**Content-Type:** `application/json`


_Schema: _

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | `mifleet-tax-response` |  |  |  |
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

## DELETE `/mifleet/tax/{id}`

**Delete MiFleet Tax Entries**

Deletes a specific tax entry from the MiFleet system.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the tax entry to be deleted. | 1010 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

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

## GET `/mifleet/tyre`

**Get MiFleet Tyre Entries**

Retrieves a detailed list of tyre entries for MiFleet. This endpoint is designed for comprehensive tracking and management of tyre-related expenses and activities.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[date_from]` | optional | `date` | Start date and time for filtering breakdown entries. Only entries recorded on or after this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[date_to]` | optional | `date` | End date and time for filtering breakdown entries. Only entries recorded on or before this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `filter[id]` | optional | integer | Optional filter to retrieve by identification number. | 67890 |
| `filter[ids]` | optional | string | Filter by multiple entry IDs, separated by commas. Suitable for batch queries. | 67890,67891 |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-tyre-response` |  |  |  |
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

## POST `/mifleet/tyre`

**Create New MiFleet Tyre Entries**

Creates a new tyre transaction entry in the MiFleet system. This endpoint allows for adding tyre entries to facilitate tracking and managing tyre costs.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Request Body

**Content-Type:** `application/json`


_Schema: array of _

### Responses

#### `200` — Successful operation.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-tyre-response` |  |  |  |
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

## PUT `/mifleet/tyre/{id}`

**Update MiFleet Tyre Transaction Entry**

Updates an existing MiFleet tyre transaction entry. This endpoint allows for modifying specific details of a tyre entry identified by its unique ID.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the tyre entry to be updated. | 1010 |

### Request Body

**Content-Type:** `application/json`


_Schema: _

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | `mifleet-tyre-response` |  |  |  |
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

## DELETE `/mifleet/tyre/{id}`

**Delete MiFleet Tyre Entries**

Deletes a specific tyre entry from the MiFleet system.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the tyre entry to be deleted. | 1010 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

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

## GET `/mifleet/vehicle-license`

**Get MiFleet Vehicle License Entries**

Retrieves a detailed list of vehicle license entries for MiFleet. This endpoint is designed for comprehensive tracking and management of vehicle license-related expenses and activities.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[date_from]` | optional | `date` | Start date and time for filtering breakdown entries. Only entries recorded on or after this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[date_to]` | optional | `date` | End date and time for filtering breakdown entries. Only entries recorded on or before this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `filter[id]` | optional | integer | Optional filter to retrieve by identification number. | 67890 |
| `filter[ids]` | optional | string | Filter by multiple entry IDs, separated by commas. Suitable for batch queries. | 67890,67891 |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-vehicle-license-response` |  | This array returns the list of vehicle license entries. |  |
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

## POST `/mifleet/vehicle-license`

**Create New MiFleet Vehicle License Entries**

Creates a new vehicle license entry in the MiFleet system. This endpoint allows for adding vehicle license entries to facilitate tracking and managing vehicle license costs.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Request Body

**Content-Type:** `application/json`


_Schema: array of _

### Responses

#### `200` — Successful operation.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `mifleet-vehicle-license-response` |  | This array returns the list of vehicle license entries. |  |
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

## PUT `/mifleet/vehicle-license/{id}`

**Update MiFleet Vehicle License Transaction Entry**

Updates an existing MiFleet vehicle license entry. This endpoint allows for modifying specific details of a vehicle license entry identified by its unique ID.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the vehicle license entry to be updated. | 1010 |

### Request Body

**Content-Type:** `application/json`


_Schema: _

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | `mifleet-vehicle-license-response` |  |  |  |
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

## DELETE `/mifleet/vehicle-license/{id}`

**Delete MiFleet Vehicle License Entries**

Deletes a specific vehicle license entry from the MiFleet system.  
  
 **Note:** MiFleet API endpoints are only accessible to main account administrators. Subusers do not have access to these APIs.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `id` | **required** | integer | Unique identifier of the vehicle license entry to be deleted. | 1010 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

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

