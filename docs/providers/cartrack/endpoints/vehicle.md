---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Vehicle
spec_version: 1.26.0622.1
---

# Vehicle

_13 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [GET `/vehicles`](#get-vehicles) — Get Vehicles List
- [GET `/vehicles/activity`](#get-vehicles-activity) — Get All Vehicles' Activity
- [GET `/vehicles/{registration}/clock`](#get-vehicles-registration-clock) — Get the Clock Reading
- [GET `/vehicles/{registration}/odometer`](#get-vehicles-registration-odometer) — Get the Odometer Reading
- [GET `/vehicles/audit`](#get-vehicles-audit) — Get Vehicle Audit History
- [POST `/vehicles/{registration}/share-location-link`](#post-vehicles-registration-share-location-link) — Generate Shareable Location URL for Vehicle by Registration
- [GET `/vehicles/{registration}/power-takeoff`](#get-vehicles-registration-power-takeoff) — Get Vehicle Power Takeoff Status
- [GET `/vehicles/{registration}/sensors/timeline`](#get-vehicles-registration-sensors-timeline) — Get Sensor Timeline for One Vehicle
- [GET `/vehicles/seat/occupancy`](#get-vehicles-seat-occupancy) — Get Vehicle's Seat Occupancy
- [GET `/vehicles/nearest`](#get-vehicles-nearest) — Get Vehicles Nearest to Point
- [GET `/vehicles/vext`](#get-vehicles-vext) — Get Vehicles Vext At Ignition OFF
- [PUT `/vehicles/{registration}`](#put-vehicles-registration) — Update a Vehicle's Details
- [GET `/vehicles/contracts`](#get-vehicles-contracts) — Get Vehicle's Contracts

## GET `/vehicles`

**Get Vehicles List**

This endpoint returns all/filtered list of vehicles with their details.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[vehicle_id]` | optional | integer | Filter by vehicle ID | 12345 |
| `filter[registration]` | optional | string | Filter vehicle registration, case insensitive, can be partial match | "GRG123" |
| `filter[manufacturer]` | optional | string | Filter by vehicle make, case insensitive, can be partial match | "Toyota" |
| `filter[model_year]` | optional | integer | Filter vehicle model year | 2025 |
| `filter[colour]` | optional | string | Filter by vehicle colour, case insensitive, can be partial match | "red" |
| `filter[chassis_number]` | optional | string | Filter by chassis number, case insensitive, can be partial match | "RGJ123" |
| `page` | optional | integer | The current page | 1 |
| `limit` | optional | integer | The number of items to display per page | 15 |

### Request Body

_No request body._

### Responses

#### `200` — successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  |  |  |
| `meta` | `pagination` |  |  |  |

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

#### `500` — Internal server error.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |


---

## GET `/vehicles/activity`

**Get All Vehicles' Activity**

This endpoints returns all of your your vehicles' total activity and break time for a given day.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[registration]` | optional | string | Filter vehicle registration, case insensitive, can be partial match | "GRG123" |
| `filter[date]` | optional | `date-only` | This will filter results for the given date | "2022-01-01" |

### Request Body

_No request body._

### Responses

#### `200` — successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  |  |  |

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

#### `500` — Internal server error.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |


---

## GET `/vehicles/{registration}/clock`

**Get the Clock Reading**

This endpoint returns the clock at the start and at the end of a given period. It also returns the current clock value. The clock is the total number of seconds of engine ON since the Cartrack tracker was first installed.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `registration` | **required** | `registration` | The vehicle's registration | ABC1234 |

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `start_timestamp` | **required** | string | Returns data within the specified time range | "2022-01-01 00:00:00" |
| `end_timestamp` | **required** | string | Returns data within the specified time range | "2022-01-01 23:59:59" |

### Request Body

_No request body._

### Responses

#### `200` — successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object | null |  |  |  |

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

#### `404` — The requested resource was not found.

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

## GET `/vehicles/{registration}/odometer`

**Get the Odometer Reading**

This endpoint returns the odometer at the start and at the end of a given period. The odometer is calculated from data sent by the device itself, if available. If the vehicle's odometer is not available, it will be calculated from GPS data.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `registration` | **required** | `registration` | The vehicle's registration | ABC1234 |

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `start_timestamp` | **required** | string | Returns data within the specified time range | "2022-01-01 00:00:00" |
| `end_timestamp` | **required** | string | Returns data within the specified time range | "2022-01-01 23:59:59" |

### Request Body

_No request body._

### Responses

#### `200` — successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object | null |  |  |  |

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

#### `404` — The requested resource was not found.

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

## GET `/vehicles/audit`

**Get Vehicle Audit History**

Retrieves a paginated list of historical changes to all vehicles in the account within a specified date range (YYYY-MM-DD).  
 Each record represents a change event — such as updates to vehicle descriptions or default driver assignments — along with the timestamp when the change occurred.  
 This allows you to review and track how vehicle details have evolved over time

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[update_ts_from]` | optional | `date` | The date filter in range to get audit logs. The date format is "YYYY-MM-DD H:i:s". | "2022-05-05 00:00:00" |
| `filter[update_ts_to]` | optional | `date` | The latest date to retrieve audit logs. The date format is "YYYY-MM-DD H:i:s". | "2022-05-05 23:59:59" |
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  |  |  |
| `meta` | `pagination` |  |  |  |

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

#### `404` — The requested resource was not found.

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

## POST `/vehicles/{registration}/share-location-link`

**Generate Shareable Location URL for Vehicle by Registration**

Generate a shareable location link for a vehicle. Each vehicle and account is limited to a maximum of 10 links.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `registration` | **required** | `registration` | The vehicle's license plate | ABC1234 |

### Request Body

The json data that needs to be processed

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `expiration_ts` | `date` | **required** | The shareable location URL expiration date-time in the future. |  |

### Responses

#### `200` — successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object | null | **required** |  |  |

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

#### `404` — The requested resource was not found.

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

## GET `/vehicles/{registration}/power-takeoff`

**Get Vehicle Power Takeoff Status**

This endpoint returns the events when a vehicle's power take-off was active or inactive. You can measure the active time of your vehicles. These vehicles are:

* Running a water pump on a fire engine or water truck
* Running a truck mounted hot water extraction machine for carpet cleaning (driving vacuum blower and high-pressure solution pumps)
* Powering a blower system used to move dry materials such as cement
* Powering a vehicle-integrated air compressor system
* Raising a dump truck bed
* Operating the mechanical arm on a bucket truck used by electrical maintenance personnel or cable TV maintenance crews
* Operating a winch on a tow truck
* Operating the compactor on a garbage truck
* Operating a Boom/Grapple truck
* Operating a truck mounted tree spade and lift-mast assembly

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `registration` | **required** | `registration` | The vehicle's registration | ABC1234 |

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `start_date` | optional | `date` | The start date to filter records. The date format is "YYYY-MM-DD HH:mm:ss". | "2022-05-01 00:00:00" |
| `end_date` | optional | `date` | The end date to filter records. The date format is "YYYY-MM-DD HH:mm:ss". | "2022-05-01 23:59:59" |
| `page` | optional | integer | The current page | 1 |
| `limit` | optional | integer | The number of items to display per page | 15 |

### Request Body

_No request body._

### Responses

#### `200` — successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  | The list of events generated during that period |  |
| `meta` | `pagination` |  |  |  |

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

#### `404` — The requested resource was not found.

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

## GET `/vehicles/{registration}/sensors/timeline`

**Get Sensor Timeline for One Vehicle**

Retrieves the historical timeline of sensor readings for a given vehicle registration number. Results are ordered by event time descending. The range between `filter[start_timestamp]` and `filter[end_timestamp]` cannot exceed 31 days.
For a full list of supported sensors, response field details, and country-specific sensor value mappings (including the Taxi sensor), see the [Vehicle Sensors Services guide](https://developer.cartrack.com/docs/fleet-api-general/services/vehicle-sensors-services).


### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `registration` | **required** | `registration` | The vehicle's registration number. | ABC-12345 |

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[sensor]` | **required** | string | The sensor to filter by. Supported values: `FUEL`, `TAXI`, `EV_BATTERY`, `EV_BATTERY_CHARGING_STATUS`, `EV_RANGE`, `EV_CONSUMPTION`. | TAXI |
| `start_timestamp` | **required** | string | Returns data within the specified time range | "2022-01-01 00:00:00" |
| `end_timestamp` | **required** | string | Returns data within the specified time range | "2022-01-01 23:59:59" |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object | **required** |  |  |
| `meta` | `pagination` | **required** |  |  |

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

#### `404` — The requested resource was not found.

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

## GET `/vehicles/seat/occupancy`

**Get Vehicle's Seat Occupancy**

This endpoint returns the latest seat occupancy status for your vehicles. The vehicle must have the seat sensor (sensor type id 63) and Cartrack telematic device must be connected to the CAN bus.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `filter[vehicle_id]` | optional | integer | Optional filter to retrieve by vehicle ID. | 12345 |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  | List of vehicle's seat occupancy. |  |
| `meta` | `pagination` |  |  |  |

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

#### `404` — The requested resource was not found.

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

## GET `/vehicles/nearest`

**Get Vehicles Nearest to Point**

Get the nearest vehicles for a given latitude, longitude, radius (meters)

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `longitude` | **required** | number | The longitude | 103.123456 |
| `latitude` | **required** | number | The latitude | 1.345877 |
| `filter[max_distance]` | optional | integer | The radius in meters | 100 |
| `filter[include_many_registrations]` | optional | string | An optional vehicle registration list (comma seperated), to limit the results to certain vehicles | FBP111G,FBP112S,FBP113Q |
| `filter[exclude_many_registrations]` | optional | string | An optional vehicle registration list (comma seperated), to exclude certain vehicles from the results | BG650001,BG650002,BG650003 |
| `google_api_key` | optional | string | An optional Google API Key, to fetch the route information from vehicle position to the given coordinates. If no key is passed, the properties inside "to_destination_google_api" object will be null. The key will be used to call for each vehicle in the response. You can make use of the exclusion/inclusion parameters to reduce the key usage to specific vehicles only. They key is not stored in our systems. | 8uScO1Y5OzM3cLllh5gQwTTtgp7FhmxwUZ9Ig8Oy |
| `limit` | optional | integer | In order to limit the number of vehicles for which Cartrack will invoke the Google API and calculate the road distance to the destination, you can specify an optional limit to fetch a limited number of vehicles closest to the given latitude and longitude based on the Euclidean distance. This parameter allows you to limit Google API usage and reduce costs. | 0 |

### Request Body

_No request body._

### Responses

#### `200` — successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  | This array returns the list of vehicles |  |

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

## GET `/vehicles/vext`

**Get Vehicles Vext At Ignition OFF**

In telematics, VEXT (Vehicle External Voltage) refers to the voltage supplied to a telematics device from the vehicle's electrical system.  
  
 This endpoint returns the VEXT data of your vehicles at ignition OFF. For the latest data at ignition ON, please refer to GET /vehicles/status.  
  
 **Note:** This endpoint is subject to a rate limit, allowing a maximum of 10 request per minute.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  |  |  |
| `meta` | `pagination` |  |  |  |

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

#### `404` — The requested resource was not found.

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

## PUT `/vehicles/{registration}`

**Update a Vehicle's Details**

This endpoint is to update information relating to the vehicle.  
  
 For instance, you can update the GPS odometer (in meters). If your Cartrack device is set to use the GPS odometer, the reading won't be 100% accurate. You may want to reset the odometer reading from time to time with the actual dashboard reading. If your Cartrack device is using the canbus odometer, please ignore this field. Please get in touch with our technicians if you have any questions.  
  
 The custom fields 1 to 7 can be used for your operations (for instance, adding shipment references to your vehicles for deliveries or any other information from your systems). You can label the custom fields in your fleet web account. They are displayed on the vehicle detail page.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `registration` | **required** | `registration` | The vehicle's registration | ABC1234 |

### Request Body

Vehicle information to update

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `default_timezone` | string |  | The vehicle's default timezone | "Asia/Singapore" |
| `monthly_mileage_limit` | integer | null |  | The monthly mileage limit in km | 1300 |
| `tolling_tag_id` | string | null |  | The tolling tag id to be assigned to the vehicle | "12345" |
| `vehicle_name` | string | null |  | A name that can be given to the vehicle | "Big car" |
| `client_vehicle_description` | string | null |  | A description that can be given to the vehicle | "A description that can be given to the vehicle" |
| `client_vehicle_description2` | string | null |  | A second description that can be given to the vehicle | "A second description that can be given to the vehicle" |
| `client_vehicle_description3` | string | null |  |  | "A third description that can be given to the vehicle" |
| `licence_code` | string | null |  | The license code of the vehicle. Please check the Vienna convention of 1968 for the accepted license codes (page 71) | "B" |
| `licence_issued_date` | string | null |  | The license issued date | "2020-01-01" |
| `licence_expiry_date` | string | null |  | The license expiry date | "2025-01-01" |
| `default_driver` | string | null |  | The default driver to be assigned to this vehicle. Trips will be assigned to this driver by default, when no driver tag was used. | "62462fcf-0938-11ec-8c4d-a4bf016cd6b2" |
| `home_geofence` | string | null |  | The default geofence to be assigned to this vehicle. | "62462fcf-0938-11ec-8c4d-a4bf016cd6b2" |
| `gps_odometer` | number |  | A field to reset the GPS odometer (in meters). If your Cartrack device is set to use the GPS odometer, the reading won't be 100% accurate. You may want to reset the odometer reading from time to time with the actual dashboard reading. If your Cartrack device is using the canbus odometer, please ignore this field. Please get in touch with our technicians if you have any questions. | 100000 |
| `vehicle_type_id` | integer |  | The vehicle type ID and its corresponding description: <table> <thead> <tr> <th>ID</th> <th>Description</th> </tr> </thead> <tbody> <tr><td>0</td><td>Default</td></tr> <tr><td>1</td><td>Motorbike</td></tr> <tr><td>2</td><td>Small Car</td></tr> <tr><td>3</td><td>Sedan Car</td></tr> <tr><td>4</td><td>4X4</td></tr> <tr><td>5</td><td>Van</td></tr> <tr><td>6</td><td>Small Truck</td></tr> <tr><td>7</td><td>Large Truck</td></tr> <tr><td>8</td><td>Small Machine</td></tr> <tr><td>9</td><td>Large Machine</td></tr> <tr><td>10</td><td>Bus</td></tr> <tr><td>11</td><td>Golf Cart / Buggy</td></tr> <tr><td>12</td><td>Truck Concrete Pump</td></tr> <tr><td>13</td><td>Truck Mixer</td></tr> <tr><td>14</td><td>Mobile Crane</td></tr> <tr><td>15</td><td>Boat</td></tr> <tr><td>16</td><td>Generator</td></tr> <tr><td>17</td><td>Crane</td></tr> <tr><td>18</td><td>Static Pump</td></tr> <tr><td>19</td><td>Trailer</td></tr> <tr><td>20</td><td>WiFi Units</td></tr> <tr><td>21</td><td>Pickup Truck</td></tr> <tr><td>22</td><td>Ambulance</td></tr> <tr><td>23</td><td>Water Truck</td></tr> <tr><td>24</td><td>Fire Truck</td></tr> <tr><td>25</td><td>Road Roller</td></tr> <tr><td>26</td><td>Grader</td></tr> <tr><td>27</td><td>Forklift</td></tr> <tr><td>28</td><td>Tractor</td></tr> <tr><td>29</td><td>Dump Truck</td></tr> <tr><td>30</td><td>Backhoe</td></tr> <tr><td>31</td><td>Loader</td></tr> <tr><td>32</td><td>Lorry</td></tr> <tr><td>33</td><td>Lorry Crane</td></tr> <tr><td>34</td><td>Tow Truck</td></tr> <tr><td>35</td><td>Uncharacterized</td></tr> <tr><td>36</td><td>Patrol</td></tr> <tr><td>37</td><td>Prisoner Transport</td></tr> <tr><td>38</td><td>Bullet Proof</td></tr> <tr><td>39</td><td>Jetski</td></tr> <tr><td>40</td><td>Railway Car</td></tr> <tr><td>41</td><td>Drilling Rig</td></tr> <tr><td>42</td><td>RTX Machine</td></tr> <tr><td>43</td><td>Skid Steer</td></tr> <tr><td>44</td><td>Excavator</td></tr> </tbody> </table> | 23 |
| `custom_fields` | object |  | The custom fields for the vehicle (up to a max of 7).       Only applicable if custom fields are configured in your company settings on Fleet Web. |  |

### Responses

#### `200` — successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  |  |  |
| `meta` | string |  |  | "Vehicle information updated successfully" |

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

#### `404` — The requested resource was not found.

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

## GET `/vehicles/contracts`

**Get Vehicle's Contracts**

This endpoint is to get vehicle's which are active in given time period.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[registration]` | optional | string | Filter vehicle registration, case insensitive, can be partial match | "GRG123" |
| `filter[active_from]` | optional | `date` | Filter by driver active start date. Must be in the format of YYYY-MM-DD HH:MM:SS | 2025-07-21 10:31:30 |
| `filter[active_to]` | optional | `date` | Filter by driver active end date. Must be in the format of YYYY-MM-DD HH:MM:SS | 2025-07-21 10:31:30 |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  |  |  |
| `meta` | `pagination` |  |  |  |

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

#### `404` — The requested resource was not found.

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

