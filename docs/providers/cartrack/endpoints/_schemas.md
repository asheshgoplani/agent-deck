---
source: https://developer.cartrack.com/openapi/openapi.yaml
---

# Shared Schemas

_Reusable models referenced across endpoints. Generated from the OpenAPI spec v1.26.0622.1._


## `aemp-datetime` {#schema-aemp-datetime}

ISO 8601 formatted datetime with timezone information

_Type: string_


**Example:**

```json
2025-05-02T13:22:19+08:00
```


## `aemp-equipment-header` {#schema-aemp-equipment-header}

Standard AEMP equipment header information

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `EquipmentID` | `aemp-equipment-id` | **required** | The Vehicle Registration or Equipment Identifier |  |
| `SerialNumber` | string | **required** | The Vehicle Chassis Number or Serial Number | "1231231" |
| `OEMName` | string | **required** | The Original Equipment Manufacturer Name | "Mazda" |
| `Model` | string | **required** | The Equipment Model Name | "MAZDA3 HATCHBACK 1.5 AT DELUXE EU6" |
| `UnitInstallDateTime` | `aemp-datetime` |  | Telematics Unit Installation Date and Time |  |

## `aemp-equipment-id` {#schema-aemp-equipment-id}

Equipment identifier following AEMP ISO15143-3 standard

_Type: string_


**Example:**

```json
SNN7868C-2K
```


## `aemp-location` {#schema-aemp-location}

Geographic location information following AEMP standard

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `DateTime` | `aemp-datetime` | **required** | The timestamp when location was recorded |  |
| `Latitude` | number | **required** | Latitude coordinate in decimal degrees | -26.123456 |
| `Longitude` | number | **required** | Longitude coordinate in decimal degrees | 28.123456 |
| `Altitude` | number |  | Altitude above sea level | 1500.5 |
| `AltitudeUnits` | string |  | Unit of altitude measurement | "meter" |

## `alert-name` {#schema-alert-name}

The alert's name

_Type: string_


**Example:**

```json
This is an API alert for Gofence 0724
```


## `carwatch-status` {#schema-carwatch-status}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `vehicle_id` | integer |  | The id of the vehicle | 123456 |
| `registration` | string |  | The registration of the vehicle | "12ABC1234X345" |
| `is_active` | boolean |  | The current status | true |

## `chassis-number` {#schema-chassis-number}

The chassis number of the vehicle.

_Type: string | null_


**Example:**

```json
1HGCM82633A123456
```


## `contact-type-id` {#schema-contact-type-id}

The contact type ID determines the type of alert notification.  
1 represents E-Mail Message  
2 represents Short Message Service  
3 represents RSS Feeds  
4 represents Alert Center

_Type: integer_


**Example:**

```json
1
```


## `country-id` {#schema-country-id}

Country ID for filtering customers

_Type: integer_


**Example:**

```json
193
```


## `customer-base` {#schema-customer-base}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `customer_id` | string |  | Unique identifier (UUID) of the customer | "f3a42187-0c6e-11ec-aa41-a4bf016cd6b2" |
| `customer_name` | `customer-name` |  |  |  |
| `email` | `email` |  | The customer's email |  |
| `contact_code` | string | null |  | The contact country code | "63" |
| `contact_number` | string | null |  | The mobile device number | "92729821" |
| `address_line_1` | string | null |  | The address line 1 | "Mcnair Road" |
| `address_line_2` | string | null |  | The address line 2 | "Boonkeng Road" |
| `postal_code` | string | null |  | The postal code | "320119" |
| `country_id` | `country-id` |  | The country ID |  |
| `latitude` | number | null |  | The latitude | 1.31972 |
| `longitude` | number | null |  | The longitude | 103.85687 |
| `subuser_id` | string | null |  | The subuser_id | "f3a42187-0c6e-11ec-aa41-a4bf016cd6b2" |
| `client_reference` | string | null |  | The client reference. | "#AB123456" |
| `create_ts` | string |  | The creation timestamp | "2023-01-01 12:00:00" |
| `update_ts` | string | null |  | The last update timestamp | "2023-01-01 12:00:00" |

## `customer-name` {#schema-customer-name}

The customer's name

_Type: string_


**Example:**

```json
Mark
```


## `date` {#schema-date}

_Type: string_


**Example:**

```json
2023-01-01 12:00:00
```


## `date-only` {#schema-date-only}

Date only, without time

_Type: string_


**Example:**

```json
2023-01-01
```


## `delivery-driver` {#schema-delivery-driver}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `delivery_driver_id` | string |  | Unique UUID of the delivery driver | "f3a42187-0c6e-11ec-aa41-a4bf016cd6b2" |
| `create_ts` | string |  |  | "2023-01-01 12:00:00" |
| `update_ts` | string | null |  |  | "2023-01-01 12:00:00" |
| `first_name` | string |  | Driver first name | "Mark Steven" |
| `last_name` | string | null |  | Driver last name | "Jobs" |
| `phone_code` | `phone-code` |  |  |  |
| `phone_number` | string |  | Phone number | "94281277" |
| `email` |  |  |  |  |
| `registration` |  |  |  |  |
| `login_username` | string | null |  |  |  |
| `driver_status_id` | `driver-status-id` |  |  |  |
| `user_id` | `id` |  |  |  |
| `last_login_ts` | string | null |  |  | "2023-01-01 12:00:00" |
| `is_active` | boolean |  | Flag to determine if the account is active | true |
| `latitude` | number | null |  | Driver latitude position | 1.3201800174182 |
| `longitude` | number | null |  | Driver longitude position | 103.88890856757 |
| `subuser_id` | string | null |  | Subuser ID | "62462fcf-0938-11ec-8c4d-a4bf016cd6b2" |
| `login_type` | integer |  | Login type identifier | 1 |
| `remember_token` | string |  | Remember token hash value |  |
| `shift_time_start` |  |  | Start time of driver's shift in HH:MM:SS+TZ format |  |
| `shift_time_end` |  |  | End time of driver's shift in HH:MM:SS+TZ format |  |
| `start_location_customer_id` | string | null |  | Start location customer ID | "f3a42187-0c6e-11ec-aa41-a4bf016cd6b2" |
| `end_location_customer_id` | string | null |  | End location customer ID | "f3a42187-0c6e-11ec-aa41-a4bf016cd6b2" |
| `is_planning` | boolean |  | Flag to determine if planning | true |
| `fleet_driver_id` | string | null |  | Must be driver id. Format in uuid | "62462fcf-0938-11ec-8c4d-a4bf016cd6b2" |

## `delivery-driver-response` {#schema-delivery-driver-response}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `delivery_driver_id` | string |  | Unique UUID of the delivery driver | "f3a42187-0c6e-11ec-aa41-a4bf016cd6b2" |
| `create_ts` | string |  |  | "2023-01-01 12:00:00" |
| `update_ts` | string | null |  |  | "2023-01-01 12:00:00" |
| `first_name` | string |  | Driver first name | "Mark Steven" |
| `last_name` | string | null |  | Driver last name | "Jobs" |
| `phone_code` | `phone-code` |  |  |  |
| `phone_number` | string |  | Phone number | "94281277" |
| `email` |  |  |  |  |
| `registration` |  |  |  |  |
| `login_username` | string | null |  |  |  |
| `driver_status_id` | `driver-status-id` |  |  |  |
| `user_id` | `id` |  |  |  |
| `last_login_ts` | string | null |  |  | "2023-01-01 12:00:00" |
| `is_active` | boolean |  | Flag to determine if the account is active | true |
| `latitude` | number | null |  | Driver latitude position | 1.3201800174182 |
| `longitude` | number | null |  | Driver longitude position | 103.88890856757 |
| `subuser_id` | string | null |  | Subuser ID | "62462fcf-0938-11ec-8c4d-a4bf016cd6b2" |
| `login_type` | integer |  | Login type identifier | 1 |
| `remember_token` | string |  | Remember token hash value |  |
| `shift_time_start` |  |  | Start time of driver's shift in HH:MM:SS+TZ format |  |
| `shift_time_end` |  |  | End time of driver's shift in HH:MM:SS+TZ format |  |
| `start_location_customer_id` | string | null |  | Start location customer ID | "f3a42187-0c6e-11ec-aa41-a4bf016cd6b2" |
| `end_location_customer_id` | string | null |  | End location customer ID | "f3a42187-0c6e-11ec-aa41-a4bf016cd6b2" |
| `is_planning` | boolean |  | Flag to determine if planning | true |
| `fleet_driver_id` | string | null |  | Must be driver id. Format in uuid | "62462fcf-0938-11ec-8c4d-a4bf016cd6b2" |
| `is_online` | boolean |  | (REMOVED) Use driver_status_id (check for NOT in Offline status) instead | false |
| `max_weight` | number |  | Maximum weight the driver can carry | 1000 |
| `max_volume` | number |  | Maximum volume the driver can carry | 1000 |
| `special_equipment` | array | null |  | List of special equipment the driver can handle |  |

## `delivery-job-base` {#schema-delivery-job-base}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `job_id` |  |  | Unique identifier of the delivery job |  |
| `create_ts` | `timestamp` |  |  |  |
| `update_ts` |  |  |  |  |
| `order_id` | string | null |  | A system generated order no. | "20210908000021" |
| `reference_number` | string | null |  | Reference number | "ABC123" |
| `scheduled_delivery_ts` | string |  | Delivery scheduled timestamp | "2023-01-01 12:00:00" |
| `delivery_driver_id` | string | null |  | Unique UUID of the delivery driver | "f3a42187-0c6e-11ec-aa41-a4bf016cd6b2" |
| `assigned_ts` | string | null |  | Assigned timestamp | "2023-01-01 12:00:00" |
| `schedule_type_id` | `schedule-type-id` |  |  |  |
| `job_status_id` | `job-status-id` |  |  |  |
| `user_id` | `id` |  |  |  |
| `is_visible` | boolean |  | Flag to determine if job is visible in mobile | true |
| `is_deleted` | boolean |  | Flag to determine if job deleted | true |
| `job_type_id` | `job-type-id` |  |  |  |
| `is_planning` | boolean |  | Flag to determine if the job is plan | false |
| `allow_add_item` | boolean |  | Allow user item | true |
| `notes` | string | null |  | Notes |  |
| `subuser_id` | string | null |  | Subuser ID | "72856440-1047-11ec-a6d3-a4bf016cd6b2" |
| `allowed_to_start_at` | string | null |  | Allowed to start timestamp | "2021-09-18 12:30:00" |
| `send_to_driver_at` | string | null |  | Send to driver timestamp | "2023-01-01 12:00:00" |

## `delivery-job-item` {#schema-delivery-job-item}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `job_item_id` | integer |  | Job item ID |  |
| `create_ts` | `date` |  | Created timestamp |  |
| `update_ts` | `date` |  | Updated timestamp |  |
| `job_id` | integer |  | Unique identifier of the delivery job to be completed |  |
| `item_type_id` | `job-item-type-id` |  |  |  |
| `weight_measure_id` | integer |  | 1 = KG |  |
| `description` | string |  | The item description |  |
| `weight` | number |  | The item weight |  |
| `job_item_status_id_pickup` | integer |  | Job item status ID for pickup. 1 = Uncompleted, 2 = Completed OK, 3 = Rejected, 4 = Partial Rejected, 5 = Damage Return |  |
| `job_item_status_id_dropoff` | integer |  | Job item status ID for dropoff    1 = Uncompleted, 2 = Completed OK, 3 = Rejected, 4 = Partial Rejected, 5 = Damage Return |  |
| `quantity` | integer |  | number of items |  |
| `length` | string | null |  | Length of item |  |
| `width` | string | null |  | Width of item |  |
| `height` | string | null |  | Height of item |  |
| `tracking_number` | string | null |  | A unique ID or number assigned to a package/parcel |  |
| `job_item_status_id_single` | integer |  | Job item status |  |
| `sku` | string | null |  | Stock Keeping Unit |  |
| `upc` | string | null |  | Universal Product Code |  |
| `todos` | array of `delivery-job-todo` |  |  |  |

## `delivery-job-leg` {#schema-delivery-job-leg}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `leg_id` | integer |  | Leg ID | 114557 |
| `start_stop_id` | integer | null |  | The start stop id of the leg record | 1319116 |
| `end_stop_id` | integer | null |  | The end stop id of the leg record | 1319129 |
| `create_ts` | `timestamp-with-offset` |  | The created timestamp of the leg record |  |
| `projected_polyline` | string | null |  | The projected polyline of the leg record used to plot route lines on a map | "u_aoAoukbeE????????????nmAhG????????????????????????????????" |
| `projected_distance` | integer | null |  | The projected distance of the leg record in meters | 10 |
| `last_estimated_travel_time` | integer | null |  | The estimated travel time of the leg record in seconds | 360 |
| `driver_travel_polyline` | string | null |  | The actual travel polyline of the leg record used to plot route lines on a map | "u_aoAoukbeE????????????nmAhG????????????????????????????????" |
| `driver_travel_distance` | integer | null |  | The actual travel distance in meters | 500 |
| `driver_travel_time` | integer | null |  | The actual travel time of the leg record in seconds | 360 |

## `delivery-job-plan` {#schema-delivery-job-plan}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `plan_id` | `plan-id` |  |  |  |
| `name` | string | null |  | Plan name |  |
| `plan_state_id` | `plan-state-id` |  |  |  |
| `create_ts` | `date` |  | Created timestamp |  |
| `update_ts` | `date` |  | Updated timestamp |  |
| `last_jobs_release_ts` | string | null |  | Last jobs release timestamp |  |
| `dtstart` | string | null |  | Start date time |  |
| `paused_since_ts` | string | null |  | Paused since timestamp |  |
| `cloned_from_plan_id` | integer | null |  | Cloned from plan ID |  |
| `show_jobs_immediately` | boolean |  | Show jobs immediately flag |  |
| `assigned_ts` | string | null |  | Assigned timestamp |  |
| `target_driver_id` | string | null |  | Target driver ID (UUID) |  |
| `scheduled_time` | string | null |  | Scheduled time |  |
| `rrule` | string | null |  | Recurrence rule |  |

## `delivery-job-response` {#schema-delivery-job-response}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `job_id` |  |  | Unique identifier of the delivery job |  |
| `create_ts` | `timestamp` |  |  |  |
| `update_ts` |  |  |  |  |
| `order_id` | string | null |  | A system generated order no. | "20210908000021" |
| `reference_number` | string | null |  | Reference number | "ABC123" |
| `scheduled_delivery_ts` | string | null |  | Delivery scheduled timestamp | "2023-01-01 12:00:00" |
| `delivery_driver_id` | string | null |  | Unique UUID of the delivery driver | "f3a42187-0c6e-11ec-aa41-a4bf016cd6b2" |
| `assigned_ts` | string | null |  | Assigned timestamp | "2023-01-01 12:00:00" |
| `schedule_type_id` | `schedule-type-id` |  |  |  |
| `job_status_id` | `job-status-id` |  |  |  |
| `user_id` | `id` |  |  |  |
| `is_visible` | boolean |  | Flag to determine if job is visible in mobile | true |
| `is_deleted` | boolean |  | Flag to determine if job deleted | true |
| `job_type_id` | `job-type-id` |  |  |  |
| `is_planning` | boolean |  | Flag to determine if the job is plan | false |
| `allow_add_item` | boolean |  | Allow user item | true |
| `notes` | string | null |  | Notes |  |
| `subuser_id` | string | null |  | Subuser ID | "62462fcf-0938-11ec-8c4d-a4bf016cd6b2" |
| `allowed_to_start_at` | string | null |  | Allowed to start timestamp | "2021-09-18 12:30:00" |
| `send_to_driver_at` | string | null |  | Send to driver timestamp | "2021-09-18 12:30:00" |
| `driver` |  |  | Driver details |  |
| `stops` | array of  |  | Stops of this job |  |
| `items` | array of `delivery-job-item` |  | Items of this job |  |
| `labels` | array of string |  | Labels for the job |  |
| `legs` | array of `delivery-job-leg` |  | The leg records of the job from one stop\_id(start\_stop\_id) to another(end\_stop\_id).    The routes plotted by the projected\_polyline and driver\_travel\_polyline attribute value can be viewed using this portal    *[Valhalla Polyline Demo](https://valhalla.github.io/demos/polyline/?unescape=false&polyline6=true)* |  |
| `plans` | array of `delivery-job-plan` |  | The Plan where the job belongs to or where the plan was added to |  |
| `special_equipment` | `special-equipment` |  | Special equipment required for the job |  |

## `delivery-job-stop` {#schema-delivery-job-stop}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `stop_id` | integer |  | Stop ID |  |
| `create_ts` | `date` |  | Created timestamp |  |
| `job_id` | `job-id` |  |  |  |
| `stop_type_id` | `stop-type-id` |  |  |  |
| `stop_status_id` | `stop-status-id` |  |  |  |
| `country_id` | `country-id` |  |  |  |
| `customer_id` | string | null |  | Unique identifier (UUID) of the customer |  |
| `user_id` | `user-id` |  |  |  |
| `ordering` | integer | null |  | Ordering of stops |  |
| `contact_number` | string | null |  | The contact number |  |
| `contact_code` | `phone-code` |  |  |  |
| `email` | string | null |  | The contact email address |  |
| `address_line_1` | string | null |  | Address line 1 |  |
| `address_line_2` | string | null |  | Address line 2 |  |
| `postal_code` | string | null |  | The address postal code |  |
| `latitude` | number | null |  | The latitude of the delivery stop |  |
| `longitude` | number | null |  | The longitude of the delivery stop |  |
| `note` | string | null |  | Additional notes for the delivery stop |  |
| `status_remarks` | string | null |  | Status information |  |
| `todo_complete_ts` | string | null |  | Todo complete timestamp | "2023-01-01 12:00:00" |
| `activity_started_ts` | string | null |  | Activity started timestamp | "2023-01-01 12:00:00" |
| `activity_started_coordinates` | string | null |  | Activity started coordinates | "2023-01-01 12:00:00" |
| `activity_arrived_ts` | string | null |  | Activity arrived timestamp | "2023-01-01 12:00:00" |
| `activity_arrived_coordinates` | string | null |  | Activity arrived coordinates | "2023-01-01 12:00:00" |
| `activity_completed_ts` | string | null |  | Activity completed timestamp | "2023-01-01 12:00:00" |
| `driver_ack_at` | string | null |  | Timestamp at which the driver acknowledged this stop. Returns null if the stop has not been acknowledged, or if acknowledgement notifications are not enabled for this account. | "2023-01-01 12:00:00" |
| `activity_completed_coordinates` | string | null |  | Activity completed coordinates | "2023-01-01 12:00:00" |
| `customer_name` | string | null |  | Customer name |  |
| `priority` | integer | null |  | Priority value |  |
| `duration` | integer | null |  | Duration |  |
| `has_custom_priority` | boolean |  | Has custom priority flag |  |
| `subuser_id` | `subuser-id` |  |  |  |
| `todos` | array of `delivery-stop-todo` |  |  |  |
| `delivery_windows` | array of object |  | Delivery time windows for the stop. Each combination of `time_from` and `time_to` is unique. |  |
| `expected_arrival_ts` | string | null |  | Expected arrival timestamp |  |

## `delivery-job-todo` {#schema-delivery-job-todo}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `job_item_todo_id` | integer |  |  |  |
| `create_ts` | `date` |  | Created timestamp |  |
| `update_ts` | `date` |  | Updated timestamp |  |
| `job_item_id` | integer |  |  |  |
| `todo_type_id` | integer |  | Todo type ID    | todo\_type\_id | Description | Valid For item\_type\_id | | --- | --- | --- | | 1 | Get Signature | 1, 2, 3 | | 2 | Take Photo (POD) | 1, 2, 3 | | 3 | Scan to Attach | 1, 3 | | 5 | Note | 1, 2, 3 | |  |
| `todo_status_id` | integer |  | Todo status ID    | todo\_status\_id | Description | Valid for todo\_type\_id | | --- | --- | --- | | 1 | Customer Not Show | 1 | | 2 | Refuse to Sign | 1 | | 3 | Others | 1 | | 4 | No Response | 2 | | 5 | No Person at Home | 2 | | 6 | Others | 2 | | 7 | Technical Issue | 3 | | 8 | Others | 3 | | 9 | Completed OK | 1, 2, 3 | |  |
| `status_remarks` | string | null |  | Status information |  |
| `complete_ts` | string |  | Completed timestamp | "2023-01-01 12:00:00" |
| `is_required` | boolean |  | The flag for required |  |
| `stop_type_id` | integer |  | Stop type ID    | stop\_type\_id | Description | | --- | --- | | 1 | Pickup | | 2 | Dropoff | | 3 | Single | |  |
| `tag` | string | null |  | The tag for the todo |  |
| `ordering` | integer | null |  | Ordering of item |  |
| `description` | string | null |  | The todo description |  |
| `note` | string | null |  | Additional notes for the todo |  |
| `latitude` | number | null |  | The latitude of the todo location | 1.3521 |
| `longitude` | number | null |  | The longitude of the todo location | 103.8198 |
| `images` | array of  |  |  |  |

## `delivery-stop-todo` {#schema-delivery-stop-todo}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `stop_todo_id` | integer |  | Unique identifier of the stop todo |  |
| `create_ts` | `date` |  | Created timestamp |  |
| `update_ts` | string |  | Updated timestamp | "2023-01-01 12:00:00" |
| `stop_id` | integer |  | Delivery stop ID |  |
| `todo_type_id` | `stop-todo-type-id` |  |  |  |
| `todo_status_id` | `stop-todo-status-id` |  |  |  |
| `status_remarks` | string | null |  | Status information |  |
| `complete_ts` | string |  | Completed timestamp | "2023-01-01 12:00:00" |
| `is_required` | boolean |  | The required flag |  |
| `description` | string | null |  | Client description |  |
| `note` | string | null |  | Client note |  |
| `images` | array of  |  |  |  |

## `delivery-stops` {#schema-delivery-stops}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `stop_id` | integer |  | Delivery stop ID |  |
| `create_ts` | `date` |  | Created timestamp |  |
| `update_ts` | string |  | Updated timestamp | "2023-01-01 12:00:00" |
| `job_id` | `job-id` |  |  |  |
| `stop_type_id` | `stop-type-id` |  |  |  |
| `stop_status_id` | `stop-status-id` |  |  |  |
| `country_id` | integer |  | Country ID |  |
| `customer_id` | string |  | Unique identifier (UUID) of the customer | "f3a42187-0c6e-11ec-aa41-a4bf016cd6b2" |
| `user_id` | `id` |  | The currently logged in user. |  |
| `ordering` | integer |  | Ordering of stops |  |
| `contact_number` | string | null |  | The contact country code |  |
| `contact_code` | string | null |  | The mobile device number |  |
| `email` | string | null |  | The contact email address |  |
| `address_line_1` | string | null |  | Address line 1 |  |
| `address_line_2` | string | null |  | Address line 2 |  |
| `postal_code` | string | null |  | The address postal code |  |
| `latitude` | number | null |  | The latitude of the delivery stop |  |
| `longitude` | number | null |  | The longitude of the delivery stop |  |
| `note` | string | null |  | Additional notes for the delivery stop |  |
| `status_remarks` | string | null |  | Status information |  |
| `todo_complete_ts` | string | null |  | This field is deprecated. Please use stop.todos.complete_ts instead. |  |
| `activity_started_ts` | string |  | Activity started timestamp | "2023-01-01 12:00:00" |
| `activity_arrived_ts` | string |  | Activity arrived timestamp | "2023-01-01 12:00:00" |
| `activity_completed_ts` | string |  | Activity completed timestamp | "2023-01-01 12:00:00" |
| `activity_rejected_ts` | string |  | Activity rejected timestamp | "2023-01-01 12:00:00" |
| `driver_ack_at` | string | null |  | Timestamp at which the driver acknowledged this stop. Returns null if the stop has not been acknowledged, or if acknowledgement notifications are not enabled for this account. | "2023-01-01 12:00:00" |
| `customer_name` | string |  | Customer name |  |
| `priority` | integer | null |  | Priority value |  |
| `duration` | integer | null |  | Duration |  |
| `has_custom_priority` | boolean |  | Has custom priority flag |  |
| `subuser_id` | `subuser-id` |  |  |  |
| `todos` | array of `delivery-stop-todo` |  |  |  |
| `customer` | `delivery-stops-customer` |  |  |  |
| `delivery_windows` | array of object |  | Delivery time windows for the stop. Each combination of `time_from` and `time_to` is unique. |  |

## `delivery-stops-customer` {#schema-delivery-stops-customer}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `customer_id` | string |  | Unique identifier (UUID) of the customer | "f3a42187-0c6e-11ec-aa41-a4bf016cd6b2" |
| `customer_name` | `customer-name` |  |  |  |
| `email` | `email` |  | The customer's email |  |
| `contact_code` | string | null |  | The contact country code | "63" |
| `contact_number` | string | null |  | The mobile device number | "92729821" |
| `address_line_1` | string | null |  | The address line 1 | "Mcnair Road" |
| `address_line_2` | string | null |  | The address line 2 | "Boonkeng Road" |
| `postal_code` | string | null |  | The postal code | "320119" |
| `country_id` | `country-id` |  | The country ID |  |
| `latitude` | number | null |  | The latitude | 1.31972 |
| `longitude` | number | null |  | The longitude | 103.85687 |
| `subuser_id` | string | null |  | The subuser_id | "f3a42187-0c6e-11ec-aa41-a4bf016cd6b2" |
| `create_ts` | `date` |  | The creation timestamp |  |
| `update_ts` | string | null |  | The last update timestamp | "2023-01-01 12:00:00" |

## `driver` {#schema-driver}

_Type: _


## `driver-base` {#schema-driver-base}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `first_name` | string |  | The driver's first name | "Mark" |
| `last_name` | string | null |  | The driver's last name | "Steven" |
| `email` | string | null |  | The driver's email address | "mark.steven@hotmail.com" |
| `id_number` | string | null |  | The driver's id number | "931919191" |
| `phone_number` | string | null |  | The driver's contact number | "659449912" |
| `gender` | string | null |  | The driver's gender | "female" |
| `license_number` | string | null |  | The driver's license number | "ABC1234X" |
| `license_issued_country` | string | null |  | The driver's issued license country code | "SG" |
| `license_driver_restrictions` | string | null |  | The driver's restrictions | "Weekdays only" |
| `license_points` | integer | null |  | The driver's license points | 10 |
| `license_first_issue_date` | string | null |  | The first issued date of the license | "2000-01-25" |
| `license_valid_start` | string | null |  | The start date of the license | "2000-02-01" |
| `license_valid_end` | string | null |  | The end date fo the license | "2000-12-30" |
| `employee_number` | string | null |  | The employee number of the driver | "123456" |
| `custom_fields` | object | null |  | The custom fields for the driver (up to a max of 7). |  |

## `driver-create-request` {#schema-driver-create-request}

_Type: _


## `driver-group-create-request` {#schema-driver-group-create-request}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `name` | string | **required** | The name of the driver group | "Mark's Group" |
| `description` | string |  | The description of the driver group | "This is Mark's driver group" |
| `driver_ids` | array of string |  | The list of driver ids to be added to the driver group | ["123e4567-e89b-12d3-a456-426614174000", "123e4567-e89b-12d3-a456-426614174001"] |

## `driver-group-response` {#schema-driver-group-response}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `group_id` | integer |  | The unique identifier of the driver group | 2870802 |
| `name` | string |  | The name of the driver group | "Mark's Group" |
| `description` | string |  | The description of the driver group | "This is Mark's driver group" |
| `user_id` | integer |  | The unique identifier of the user who owns the driver group | 123456 |
| `client_user_id` | string | null |  | The unique identifier of the client user who owns the driver group |  |
| `is_deleted` | boolean |  | Indicates whether the driver group is deleted | false |
| `total_members` | integer |  | The total number of drivers in the driver group | 5 |

## `driver-response` {#schema-driver-response}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `driver_id` | string |  | The driver's unique identifier | "62462fcf-0938-11ec-8c4d-a4bf016cd6b2" |
| `first_name` | string |  | The driver's first name | "Mark" |
| `last_name` | string | null |  | The driver's last name | "Steven" |
| `email` | string | null |  | The driver's email address | "mark.steven@hotmail.com" |
| `id_number` | string | null |  | The driver's id number | "931919191" |
| `phone_number` | string | null |  | The driver's contact number | "659449912" |
| `gender` | string | null |  | The driver's gender | "female" |
| `license_number` | string | null |  | The driver's license number | "ABC1234X" |
| `license_issued_country` | string | null |  | The driver's issued license country code | "SG" |
| `license_driver_restrictions` | string | null |  | The driver's restrictions | "Weekdays only" |
| `license_points` | integer | null |  | The driver's license points | 10 |
| `license_first_issue_date` | string | null |  | The first issued date of the license | "2000-01-25" |
| `license_valid_start` | string | null |  | The start date of the license | "2000-02-01" |
| `license_valid_end` | string | null |  | The end date fo the license | "2000-12-30" |
| `employee_number` | string | null |  | The employee number of the driver | "123456" |
| `custom_fields` | object | null |  | The custom fields for the driver (up to a max of 7). |  |
| `logo_image_base64` | string | null |  | Base64 encoded image string for the driver's logo or profile picture | "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+ip1sAAAAASUVORK5CYII=" |
| `status` | string |  | The status of the driver | "Active" |
| `groups` | array of object |  | The groups that the driver belongs to |  |

## `driver-status-id` {#schema-driver-status-id}

1 = Online, 2 = On Route, 3 = Not Active, 4 = Offline, 5 = On Break

_Type: integer_


**Example:**

```json
4
```


## `driver-update-request` {#schema-driver-update-request}

_Type: _


## `email` {#schema-email}

The email address

_Type: string_


**Example:**

```json
steven.jobs@apple.com
```


## `geofence-id` {#schema-geofence-id}

Geofence identification number

_Type: string_


**Example:**

```json
123e4567-e89b-12d3-a456-426614174000
```


## `group-description` {#schema-group-description}

Description of the geofence group

_Type: string | null_


**Example:**

```json
Group containing all warehouse geofences
```


## `group-id` {#schema-group-id}

Unique identifier for a geofence group

_Type: integer_


**Example:**

```json
12345
```


## `group-name` {#schema-group-name}

Name of the geofence group

_Type: string | null_


**Example:**

```json
Main Warehouse Group
```


## `hour-with-timezone` {#schema-hour-with-timezone}

Timestamp with timezone offset

_Type: string_


**Example:**

```json
12:00:00+08:00
```


## `id` {#schema-id}

User identification number

_Type: integer_


**Example:**

```json
123456
```


## `ignition-trigger-id` {#schema-ignition-trigger-id}

The trigger is currently limited to ids 1, 2 and 3.   

* 1 represents Ignition ON
* 2 represents Ignition OFF
* 3 for both Ignition ON and OFF

_Type: integer_


**Example:**

```json
2
```


## `image` {#schema-image}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `image_id` | integer |  | The image id number |  |
| `create_ts` | `date` |  | Created timestamp |  |
| `image_filename` | string |  | Image filename |  |
| `image_path` | string |  | Image path URL |  |
| `customer_name` | string |  | Customer name |  |
| `image_url` | string |  | The full path of the image url |  |

## `item-todo-type-id` {#schema-item-todo-type-id}

Description of the todo <table> <thead> <tr><th>todo_type_id</th><th>Description</th><th>Valid For item_type_id</th></tr> </thead> <tbody> <tr><td>1</td><td>Get Signature</td><td>1, 2, 3</td></tr> <tr><td>2</td><td>Take Photo (POD)</td><td>1, 2, 3</td></tr> <tr><td>3</td><td>Scan to Attach</td><td>1, 3</td></tr> <tr><td>5</td><td>Note</td><td>1, 2, 3</td></tr> </tbody> </table>

_Type: integer_


## `item-tracking-number` {#schema-item-tracking-number}

Item tracking number

_Type: string_


**Example:**

```json
TRACK-123456-SIN2
```


## `job-id` {#schema-job-id}

Job identifier

_Type: number_


**Example:**

```json
12345678
```


## `job-item-type-id` {#schema-job-item-type-id}

Item type ID  

| item\_type\_id | Description |
| --- | --- |
| 1 | Package |
| 2 | Service |
| 3 | Person |

_Type: integer_


## `job-status-id` {#schema-job-status-id}

Job status identifier  

| job\_status\_id | Description |
| --- | --- |
| 2 | Assign Later |
| 3 | Rejected/Failed |
| 4 | Assigned |
| 5 | Completed |

_Type: integer_


**Example:**

```json
2
```


## `job-type-id` {#schema-job-type-id}

Job types  

| job\_type\_id | Description |
| --- | --- |
| 1 | Pickup+Dropoff job |
| 3 | Single stop job |

_Type: integer_


**Example:**

```json
1
```


## `limit` {#schema-limit}

Number of items per page

_Type: integer_


**Example:**

```json
15
```


## `mifleet-accident` {#schema-mifleet-accident}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `accident_date` | `date` |  | Date and time of the accident. | "2023-08-23 16:59:26" |
| `accident_type` | string |  | The type of accident that took place. | "Fender Bender" |
| `accident_location` | string |  | Location where the accident took place. | "Main Road between 1st and 2nd Avenues" |
| `accident_loss_value` | number |  | Value of loss as a result of this accident. | 10000 |
| `accident_has_recoveries` | boolean |  | A boolean representing the possibility of recoveries from this accident. | true |
| `accident_process_number` | string |  | Process number assigned to this accident. | "XYZ123" |
| `accident_claim_number` | string |  | Claim number assigned to this accident. | "ABC987" |
| `third_parties` | array of `mifleet-accident-third-party` |  | An optional array of third parties involved in this accident. |  |

## `mifleet-accident-response` {#schema-mifleet-accident-response}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `id` | integer |  | Unique identifier for each transaction, aiding in tracking and referencing. | 12345 |
| `document_number` | string |  | Unique reference number for the transaction document. | "ACCIDENT-123456" |
| `document_type` | string |  | The document type. Valid values are "DOCUMENT_TYPE_INVOICE", "DOCUMENT_TYPE_CREDIT_NOTE", "DOCUMENT_TYPE_DEBIT_NOTE", or "DOCUMENT_TYPE_RECEIPT". | "DOCUMENT_TYPE_INVOICE" |
| `document_status` | string |  | The document status. Valid values are "DOCUMENT_STATUS_PENDING", "DOCUMENT_STATUS_VALIDATED", "DOCUMENT_STATUS_OVERDUE_PAYMENT", "DOCUMENT_STATUS_PAID", or "DOCUMENT_STATUS_CANCELLED". | "DOCUMENT_STATUS_PAID" |
| `supplier` | string |  | Supplier or service provider for the entry. | "ABC Towing" |
| `description` | string |  | Brief summary or description of the transaction. | "Service description" |
| `quantity` | number |  | Quantity of items or services. | 1 |
| `price` | number |  | Unit price of the item or service. | 100 |
| `net_value` | number |  | Net value before tax. | 100 |
| `tax_rate` | number |  | Tax rate for the transaction (e.g., 0.2 for 20%). | 0.15 |
| `total_value` | number |  | Total value including tax. | 115 |
| `driver` | string |  | Driver associated with the entry. | "John Doe" |
| `registration` | `registration` |  |  |  |
| `vehicle_deleted` | boolean |  | Indicates whether the vehicle related to this entry is marked as deleted. | false |
| `general_ledger_code` | number | null |  | Unique code for categorizing the transaction in the general ledger. | 54321 |
| `discount` | number | null |  | Discount amount applied to the transaction. | 0.35 |
| `accident_date` | `date` |  | Date and time of the accident. | "2023-08-23 16:59:26" |
| `accident_type` | string |  | The type of accident that took place. | "Fender Bender" |
| `accident_location` | string |  | Location where the accident took place. | "Main Road between 1st and 2nd Avenues" |
| `accident_loss_value` | number |  | Value of loss as a result of this accident. | 10000 |
| `accident_has_recoveries` | boolean |  | A boolean representing the possibility of recoveries from this accident. | true |
| `accident_process_number` | string |  | Process number assigned to this accident. | "XYZ123" |
| `accident_claim_number` | string |  | Claim number assigned to this accident. | "ABC987" |
| `third_parties` | array of `mifleet-accident-third-party` |  | An optional array of third parties involved in this accident. |  |

## `mifleet-accident-third-party` {#schema-mifleet-accident-third-party}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `name` | string |  | The name of the third party involved. | "Johnny Crash" |
| `identification` | string |  | An identification/registration number for the third party. | "LMN567" |
| `contact` | string |  | Contact details for the third party. | "+351123456789" |
| `notes` | string |  | Optional notes regarding the third party. | "This vehicle was the one crashed into." |

## `mifleet-breakdown` {#schema-mifleet-breakdown}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `towing_start_date` | `date` |  | Date and time of the breakdown issuance. | "2023-08-23T16:59:26" |
| `towing_delivery_date` | `date` |  | Date and time of the breakdown deliver. | "2023-08-23T16:59:26" |
| `breakdown_type` | string |  | Breakdown type (e.g., Towing, Tyre Change). | "Towing" |
| `towing_description` | string | null |  | Towing description | "Red light breakdown" |

## `mifleet-breakdown-response` {#schema-mifleet-breakdown-response}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `id` | integer |  | Unique identifier for the breakdown entry. | 12345 |
| `document_number` | string |  | Unique reference number for the transaction document. | "ACCIDENT-123456" |
| `document_type` | string |  | The document type. Valid values are "DOCUMENT_TYPE_INVOICE", "DOCUMENT_TYPE_CREDIT_NOTE", "DOCUMENT_TYPE_DEBIT_NOTE", or "DOCUMENT_TYPE_RECEIPT". | "DOCUMENT_TYPE_INVOICE" |
| `document_status` | string |  | The document status. Valid values are "DOCUMENT_STATUS_PENDING", "DOCUMENT_STATUS_VALIDATED", "DOCUMENT_STATUS_OVERDUE_PAYMENT", "DOCUMENT_STATUS_PAID", or "DOCUMENT_STATUS_CANCELLED". | "DOCUMENT_STATUS_PAID" |
| `supplier` | string |  | Supplier or service provider for the entry. | "ABC Towing" |
| `description` | string |  | Brief summary or description of the transaction. | "Service description" |
| `quantity` | number |  | Quantity of items or services. | 1 |
| `price` | number |  | Unit price of the item or service. | 100 |
| `net_value` | number |  | Net value before tax. | 100 |
| `tax_rate` | number |  | Tax rate for the transaction (e.g., 0.2 for 20%). | 0.15 |
| `total_value` | number |  | Total value including tax. | 115 |
| `driver` | string |  | Driver associated with the entry. | "John Doe" |
| `registration` | `registration` |  |  |  |
| `vehicle_deleted` | boolean |  | Indicates whether the vehicle associated with this transaction has been marked as deleted. |  |
| `general_ledger_code` | integer | null |  | Unique code for categorizing the transaction in the general ledger. | 54321 |
| `discount` | number | null |  | Discount amount applied to the transaction. | 0.35 |
| `towing_start_date` | `date` |  | Date and time of the breakdown issuance. | "2023-08-23T16:59:26" |
| `towing_delivery_date` | `date` |  | Date and time of the breakdown deliver. | "2023-08-23T16:59:26" |
| `breakdown_type` | string |  | Breakdown type (e.g., Towing, Tyre Change). | "Towing" |
| `towing_description` | string | null |  | Towing description | "Red light breakdown" |

## `mifleet-cleaning` {#schema-mifleet-cleaning}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `cleaning_date` | `date` |  | Date and time of the cleaning issuance. |  |
| `cleaning_type` | string |  | Specific category or type of cleaning performed. | "Interior cleaning" |

## `mifleet-cleaning-response` {#schema-mifleet-cleaning-response}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `id` | integer |  | Unique identifier for the cleaning entry. | 12345 |
| `document_number` | string |  | Unique reference number for the transaction document. | "ACCIDENT-123456" |
| `document_type` | string |  | The document type. Valid values are "DOCUMENT_TYPE_INVOICE", "DOCUMENT_TYPE_CREDIT_NOTE", "DOCUMENT_TYPE_DEBIT_NOTE", or "DOCUMENT_TYPE_RECEIPT". | "DOCUMENT_TYPE_INVOICE" |
| `document_status` | string |  | The document status. Valid values are "DOCUMENT_STATUS_PENDING", "DOCUMENT_STATUS_VALIDATED", "DOCUMENT_STATUS_OVERDUE_PAYMENT", "DOCUMENT_STATUS_PAID", or "DOCUMENT_STATUS_CANCELLED". | "DOCUMENT_STATUS_PAID" |
| `supplier` | string |  | Supplier or service provider for the entry. | "ABC Towing" |
| `description` | string |  | Brief summary or description of the transaction. | "Service description" |
| `quantity` | number |  | Quantity of items or services. | 1 |
| `price` | number |  | Unit price of the item or service. | 100 |
| `net_value` | number |  | Net value before tax. | 100 |
| `tax_rate` | number |  | Tax rate for the transaction (e.g., 0.2 for 20%). | 0.15 |
| `total_value` | number |  | Total value including tax. | 115 |
| `driver` | string |  | Driver associated with the entry. | "John Doe" |
| `registration` | `registration` |  |  |  |
| `vehicle_deleted` | boolean |  | Indicates whether the vehicle associated with this transaction has been marked as deleted. |  |
| `general_ledger_code` | integer | null |  | Unique code for categorizing the transaction in the general ledger. | 54321 |
| `discount` | number | null |  | Discount amount applied to the transaction. | 0.35 |
| `cleaning_date` | `date` |  | Date and time of the cleaning issuance. |  |
| `cleaning_type` | string | null |  | Specific category or type of cleaning performed. | "Interior cleaning" |

## `mifleet-common` {#schema-mifleet-common}

_Type: object_


## `mifleet-common-without-driver` {#schema-mifleet-common-without-driver}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `document_number` | string |  | Unique reference number for the transaction document. | "ACCIDENT-123456" |
| `document_type` | string |  | The document type. Valid values are "DOCUMENT_TYPE_INVOICE", "DOCUMENT_TYPE_CREDIT_NOTE", "DOCUMENT_TYPE_DEBIT_NOTE", or "DOCUMENT_TYPE_RECEIPT". | "DOCUMENT_TYPE_INVOICE" |
| `document_status` | string |  | The document status. Valid values are "DOCUMENT_STATUS_PENDING", "DOCUMENT_STATUS_VALIDATED", "DOCUMENT_STATUS_OVERDUE_PAYMENT", "DOCUMENT_STATUS_PAID", or "DOCUMENT_STATUS_CANCELLED". | "DOCUMENT_STATUS_PAID" |
| `supplier` | string |  | Supplier or service provider for the entry. | "ABC Towing" |
| `description` | string |  | Brief summary or description of the transaction. | "Service description" |
| `quantity` | number |  | Quantity of items or services. | 1 |
| `price` | number |  | Unit price of the item or service. | 100 |
| `net_value` | number |  | Net value before tax. | 100 |
| `tax_rate` | number |  | Tax rate for the transaction (e.g., 0.2 for 20%). | 0.15 |
| `total_value` | number |  | Total value including tax. | 115 |

## `mifleet-consumable` {#schema-mifleet-consumable}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `consumable_date` | `date` |  | Date and time of the transaction. |  |
| `consumable_type` | string |  | The type of consumable purchased. | "Brake Fluid" |

## `mifleet-consumable-response` {#schema-mifleet-consumable-response}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `id` | integer |  | Unique identifier for the consumable entry. | 12345 |
| `document_number` | string |  | Unique reference number for the transaction document. | "ACCIDENT-123456" |
| `document_type` | string |  | The document type. Valid values are "DOCUMENT_TYPE_INVOICE", "DOCUMENT_TYPE_CREDIT_NOTE", "DOCUMENT_TYPE_DEBIT_NOTE", or "DOCUMENT_TYPE_RECEIPT". | "DOCUMENT_TYPE_INVOICE" |
| `document_status` | string |  | The document status. Valid values are "DOCUMENT_STATUS_PENDING", "DOCUMENT_STATUS_VALIDATED", "DOCUMENT_STATUS_OVERDUE_PAYMENT", "DOCUMENT_STATUS_PAID", or "DOCUMENT_STATUS_CANCELLED". | "DOCUMENT_STATUS_PAID" |
| `supplier` | string |  | Supplier or service provider for the entry. | "ABC Towing" |
| `description` | string |  | Brief summary or description of the transaction. | "Service description" |
| `quantity` | number |  | Quantity of items or services. | 1 |
| `price` | number |  | Unit price of the item or service. | 100 |
| `net_value` | number |  | Net value before tax. | 100 |
| `tax_rate` | number |  | Tax rate for the transaction (e.g., 0.2 for 20%). | 0.15 |
| `total_value` | number |  | Total value including tax. | 115 |
| `registration` | `registration` |  |  |  |
| `vehicle_deleted` | boolean |  | Indicates whether the vehicle related to this consumable is marked as deleted. |  |
| `general_ledger_code` | integer | null |  | Unique code for categorizing the transaction in the general ledger. | 54321 |
| `discount` | number | null |  | Discount amount applied to the transaction. | 0.35 |
| `driver` | string |  | Driver associated with the entry. | "John Doe" |
| `consumable_date` | `date` |  | Date and time of the transaction. |  |
| `consumable_type` | string |  | The type of consumable purchased. | "Brake Fluid" |

## `mifleet-contract-financing` {#schema-mifleet-contract-financing}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `contract_status` | string |  | Current status of the contract, e.g., created, active, or concluded. | "CONTRACT_STATUS_ACTIVE" |
| `contract_date` | `date` |  | The date of the contract. |  |
| `contract_start_date` | `date` |  | The date the contract is effective. |  |
| `contract_end_date` | `date` |  | The date of conclusion of the contract. |  |
| `notes` | string | null |  | Notes on the contract, detailing the nature of products/services. | "Monthly rental financing contract for trailer." |
| `odometer` | integer | null |  | Any applicable odometer value that pertains to the contract. | 101000 |
| `payment_term` | string | null |  | The term (in days) of payments for the contract. Importantly, an integer needs to be present in the string as per example. | "30 Days" |
| `payment_method` | string | null |  | The method of payment associated to the contract. | "Direct Debit" |
| `financing_type` | string |  | The type of financing associated with the contract. | "Renting" |
| `odometer_limit` | integer | null |  | An optional odometer limit pertaining to the contract. | 111000 |
| `residual_value` | number | null |  | Any residual value payable on the contract. | 5.01 |
| `interest` | number | null |  | Interest rate on the contract. | 4.5 |

## `mifleet-contract-financing-response` {#schema-mifleet-contract-financing-response}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `id` | integer |  | Unique identifier for the contract financing entry. | 12345 |
| `supplier` | string |  | Name of the supplier or entity providing value or service. | "ABC Bank" |
| `net_value` | number |  | Net value of the contract, calculated before taxes. | 4.45 |
| `tax_rate` | number |  | Applicable tax rate for the contract, represented as a decimal fraction. | 0.2 |
| `total_value` | number |  | Total value of the contract after applying any taxes. | 4.99 |
| `registration` | `registration` |  |  |  |
| `vehicle_deleted` | boolean |  | Indicates whether the associated vehicle is deleted or not. |  |
| `general_ledger_code` | integer | null |  | Code used for categorizing this transaction in the general ledger. | 54321 |
| `contract_status` | string |  | Current status of the contract, e.g., created, active, or concluded. | "CONTRACT_STATUS_ACTIVE" |
| `contract_date` | `date` |  | The date of the contract. |  |
| `contract_start_date` | `date` |  | The date the contract is effective. |  |
| `contract_end_date` | `date` |  | The date of conclusion of the contract. |  |
| `notes` | string | null |  | Notes on the contract, detailing the nature of products/services. | "Monthly rental financing contract for trailer." |
| `odometer` | integer | null |  | Any applicable odometer value that pertains to the contract. | 101000 |
| `payment_term` | string | null |  | The term (in days) of payments for the contract. Importantly, an integer needs to be present in the string as per example. | "30 Days" |
| `payment_method` | string | null |  | The method of payment associated to the contract. | "Direct Debit" |
| `financing_type` | string |  | The type of financing associated with the contract. | "Renting" |
| `odometer_limit` | integer | null |  | An optional odometer limit pertaining to the contract. | 111000 |
| `residual_value` | number | null |  | Any residual value payable on the contract. | 5.01 |
| `interest` | number | null |  | Interest rate on the contract. | 4.5 |

## `mifleet-contract-fuel-card` {#schema-mifleet-contract-fuel-card}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `contract_status` | string |  | Current status of the contract, e.g., created, active, or concluded. | "CONTRACT_STATUS_ACTIVE" |
| `contract_date` | `date` |  | The date of the contract. |  |
| `contract_start_date` | `date` |  | The date the contract is effective. |  |
| `contract_end_date` | `date` |  | The date of conclusion of the contract. |  |
| `notes` | string |  | Notes on the contract, detailing the nature of products/services. | "This is a sample note for the fuel card contract." |
| `odometer` | integer |  | Odometer reading at the time of contract initiation. | 15000 |

## `mifleet-contract-fuel-card-response` {#schema-mifleet-contract-fuel-card-response}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `id` | integer |  | Unique identifier for the fuel card contract. | 67890 |
| `supplier` | string |  | Name of the supplier or entity providing value or service. | "Fuel Card Company" |
| `registration` | `registration` |  |  |  |
| `vehicle_deleted` | boolean |  | Indicates whether the associated vehicle is deleted or not. | false |
| `net_value` | number | null |  | Net value of the contract. | 100 |
| `tax_rate` | number | null |  | Tax rate for the contract. | 0.2 |
| `total_value` | number | null |  | Total value after taxes. | 120 |
| `general_ledger_code` | integer |  | Code used for categorizing this transaction in the general ledger. | 12345 |
| `contract_status` | string |  | Current status of the contract, e.g., created, active, or concluded. | "CONTRACT_STATUS_ACTIVE" |
| `contract_date` | `date` |  | The date of the contract. |  |
| `contract_start_date` | `date` |  | The date the contract is effective. |  |
| `contract_end_date` | `date` |  | The date of conclusion of the contract. |  |
| `notes` | string |  | Notes on the contract, detailing the nature of products/services. | "This is a sample note for the fuel card contract." |
| `odometer` | integer |  | Odometer reading at the time of contract initiation. | 15000 |
| `payment_term` | string |  | The term (in days) of payments for the contract. Importantly, an integer needs to be present in the string as per example. | "NET_30" |
| `payment_method` | string |  | The method of payment associated to the contract. | "CREDIT_CARD" |
| `card_number` | string |  | The card number associated with the contract. | "0987654321" |
| `plafond` | array | null |  | An optional array of plafonds involved with the contract. |  |

## `mifleet-contract-insurance` {#schema-mifleet-contract-insurance}

_Type: object_

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

## `mifleet-contract-insurance-response` {#schema-mifleet-contract-insurance-response}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `id` | integer |  | Unique identifier for each contract, aiding in tracking and referencing. | 67890 |
| `vehicle_deleted` | boolean |  | Indicates whether the associated vehicle is deleted or not. | false |
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

## `mifleet-contract-payment-card` {#schema-mifleet-contract-payment-card}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `payment_term` | string |  | The term (in days) of payments for the contract. Importantly, an integer needs to be present in the string as per example. | "NET_30" |
| `payment_method` | string |  | The method of payment associated to the contract. | "CREDIT_CARD" |
| `card_number` | string |  | The card number associated with the contract. | "0987654321" |
| `plafond` | array | null |  | An optional array of plafonds involved with the contract. |  |

## `mifleet-driver-cost` {#schema-mifleet-driver-cost}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `driver_cost_date` | `date` |  | Date and time of the transaction. |  |
| `driver_cost_type` | string |  | The type of driver cost incurred. | "Meal" |

## `mifleet-driver-cost-response` {#schema-mifleet-driver-cost-response}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `id` | integer |  | Unique identifier for the driver cost entry. | 12345 |
| `document_number` | string |  | Unique reference number for the transaction document. | "ACCIDENT-123456" |
| `document_type` | string |  | The document type. Valid values are "DOCUMENT_TYPE_INVOICE", "DOCUMENT_TYPE_CREDIT_NOTE", "DOCUMENT_TYPE_DEBIT_NOTE", or "DOCUMENT_TYPE_RECEIPT". | "DOCUMENT_TYPE_INVOICE" |
| `document_status` | string |  | The document status. Valid values are "DOCUMENT_STATUS_PENDING", "DOCUMENT_STATUS_VALIDATED", "DOCUMENT_STATUS_OVERDUE_PAYMENT", "DOCUMENT_STATUS_PAID", or "DOCUMENT_STATUS_CANCELLED". | "DOCUMENT_STATUS_PAID" |
| `supplier` | string |  | Supplier or service provider for the entry. | "ABC Towing" |
| `description` | string |  | Brief summary or description of the transaction. | "Service description" |
| `quantity` | number |  | Quantity of items or services. | 1 |
| `price` | number |  | Unit price of the item or service. | 100 |
| `net_value` | number |  | Net value before tax. | 100 |
| `tax_rate` | number |  | Tax rate for the transaction (e.g., 0.2 for 20%). | 0.15 |
| `total_value` | number |  | Total value including tax. | 115 |
| `general_ledger_code` | number |  | Unique code for categorizing the transaction in the general ledger. | 54321 |
| `discount` | number |  | Discount amount applied to the transaction. | 0.35 |
| `driver` | string |  | Driver associated with the entry. | "John Doe" |
| `driver_cost_date` | `date` |  | Date and time of the transaction. |  |
| `driver_cost_type` | string |  | The type of driver cost incurred. | "Meal" |

## `mifleet-driver-license` {#schema-mifleet-driver-license}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `begin_date` | `date` |  | Begin date for the validity of the license. |  |
| `expiration_date` | `date` |  | End date for the validity of the license. |  |
| `license_type` | string |  | The type of driver license. | "Trailer" |
| `license_number` | string |  | The license number associated with this license. | "ABC123" |

## `mifleet-driver-license-response` {#schema-mifleet-driver-license-response}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `id` | integer |  | Unique identifier for the driver license entry. | 12345 |
| `document_number` | string |  | Unique reference number for the transaction document. | "ACCIDENT-123456" |
| `document_type` | string |  | The document type. Valid values are "DOCUMENT_TYPE_INVOICE", "DOCUMENT_TYPE_CREDIT_NOTE", "DOCUMENT_TYPE_DEBIT_NOTE", or "DOCUMENT_TYPE_RECEIPT". | "DOCUMENT_TYPE_INVOICE" |
| `document_status` | string |  | The document status. Valid values are "DOCUMENT_STATUS_PENDING", "DOCUMENT_STATUS_VALIDATED", "DOCUMENT_STATUS_OVERDUE_PAYMENT", "DOCUMENT_STATUS_PAID", or "DOCUMENT_STATUS_CANCELLED". | "DOCUMENT_STATUS_PAID" |
| `supplier` | string |  | Supplier or service provider for the entry. | "ABC Towing" |
| `description` | string |  | Brief summary or description of the transaction. | "Service description" |
| `quantity` | number |  | Quantity of items or services. | 1 |
| `price` | number |  | Unit price of the item or service. | 100 |
| `net_value` | number |  | Net value before tax. | 100 |
| `tax_rate` | number |  | Tax rate for the transaction (e.g., 0.2 for 20%). | 0.15 |
| `total_value` | number |  | Total value including tax. | 115 |
| `general_ledger_code` | number |  | Unique code for categorizing the transaction in the general ledger. | 54321 |
| `discount` | number |  | Discount amount applied to the transaction. | 0.35 |
| `driver` | string |  | Driver associated with the entry. |  |
| `begin_date` | `date` |  | Begin date for the validity of the license. |  |
| `expiration_date` | `date` |  | End date for the validity of the license. |  |
| `license_type` | string |  | The type of driver license. | "Trailer" |
| `license_number` | string |  | The license number associated with this license. | "ABC123" |

## `mifleet-financing` {#schema-mifleet-financing}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `financing_date` | `date` |  | The date of the financing transaction. |  |

## `mifleet-financing-response` {#schema-mifleet-financing-response}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `id` | integer |  | Unique identifier for the financing entry. | 12345 |
| `document_number` | string |  | Unique reference number for the transaction document. | "ACCIDENT-123456" |
| `document_type` | string |  | The document type. Valid values are "DOCUMENT_TYPE_INVOICE", "DOCUMENT_TYPE_CREDIT_NOTE", "DOCUMENT_TYPE_DEBIT_NOTE", or "DOCUMENT_TYPE_RECEIPT". | "DOCUMENT_TYPE_INVOICE" |
| `document_status` | string |  | The document status. Valid values are "DOCUMENT_STATUS_PENDING", "DOCUMENT_STATUS_VALIDATED", "DOCUMENT_STATUS_OVERDUE_PAYMENT", "DOCUMENT_STATUS_PAID", or "DOCUMENT_STATUS_CANCELLED". | "DOCUMENT_STATUS_PAID" |
| `supplier` | string |  | Supplier or service provider for the entry. | "ABC Towing" |
| `description` | string |  | Brief summary or description of the transaction. | "Service description" |
| `quantity` | number |  | Quantity of items or services. | 1 |
| `price` | number |  | Unit price of the item or service. | 100 |
| `net_value` | number |  | Net value before tax. | 100 |
| `tax_rate` | number |  | Tax rate applied. | 0.15 |
| `total_value` | number |  | Total value including tax. | 115 |
| `registration` | `registration` |  |  |  |
| `vehicle_deleted` | boolean |  | Indicates whether the associated vehicle is deleted or not. | false |
| `general_ledger_code` | number | null |  | Unique code for categorizing the transaction in the general ledger. | 54321 |
| `discount` | number | null |  | Discount amount applied to the transaction. | 0.35 |
| `financing_date` | `date` |  | The date of the financing transaction. |  |

## `mifleet-fine` {#schema-mifleet-fine}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `infringement_date` | `date` |  | Date and time of the fine issuance. |  |
| `infringement_location` | string |  | Location of the infringement. | "Church and Main Road crossing" |
| `infringement_number` | string |  | Infringement reference number. | "QXY367480" |

## `mifleet-fine-response` {#schema-mifleet-fine-response}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `id` | integer |  | Unique identifier for the fine entry. | 12345 |
| `document_number` | string |  | Unique reference number for the transaction document. | "ACCIDENT-123456" |
| `document_type` | string |  | The document type. Valid values are "DOCUMENT_TYPE_INVOICE", "DOCUMENT_TYPE_CREDIT_NOTE", "DOCUMENT_TYPE_DEBIT_NOTE", or "DOCUMENT_TYPE_RECEIPT". | "DOCUMENT_TYPE_INVOICE" |
| `document_status` | string |  | The document status. Valid values are "DOCUMENT_STATUS_PENDING", "DOCUMENT_STATUS_VALIDATED", "DOCUMENT_STATUS_OVERDUE_PAYMENT", "DOCUMENT_STATUS_PAID", or "DOCUMENT_STATUS_CANCELLED". | "DOCUMENT_STATUS_PAID" |
| `supplier` | string |  | Supplier or service provider for the entry. | "ABC Towing" |
| `description` | string |  | Brief summary or description of the transaction. | "Service description" |
| `quantity` | number |  | Quantity of items or services. | 1 |
| `price` | number |  | Unit price of the item or service. | 100 |
| `net_value` | number |  | Net value before tax. | 100 |
| `tax_rate` | number |  | Tax rate for the transaction (e.g., 0.2 for 20%). | 0.15 |
| `total_value` | number |  | Total value including tax. | 115 |
| `driver` | string |  | Driver associated with the entry. | "John Doe" |
| `registration` | `registration` |  |  |  |
| `vehicle_deleted` | boolean |  | Indicates if the vehicle associated with the fine has been deleted. | false |
| `ct_latitude` | number | null |  | Latitude coordinate of the vehicle at the time closest to the fine transaction. | -34.123456 |
| `ct_longitude` | number | null |  | Longitude coordinate of the vehicle at the time closest to the fine transaction. | 18.123456 |
| `ct_odometer` | number | null |  | Odometer reading of the vehicle closest to the time of the fine transaction. | 15000 |
| `general_ledger_code` | number | null |  | Unique code for categorizing the transaction in the general ledger. | 54321 |
| `discount` | number | null |  | Discount amount applied to the transaction. | 0.35 |
| `fine_validation_status` | string |  | Status indicating the outcome of the fine validation process. | "FINE_VALIDATION_HIGH_RISK" |
| `infringement_date` | `date` |  | Date and time of the fine issuance. |  |
| `infringement_location` | string |  | Location of the infringement. | "Church and Main Road crossing" |
| `infringement_number` | string |  | Infringement reference number. | "QXY367480" |

## `mifleet-fuel` {#schema-mifleet-fuel}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `fuel_transaction_type` | string |  | The fuel transaction type. Valid values are "FUEL_TRANSACTION_TYPE_PRIVATE" or "FUEL_TRANSACTION_TYPE_BUSINESS". Defaults to "FUEL_TRANSACTION_TYPE_BUSINESS" if not provided. | "FUEL_TRANSACTION_TYPE_PRIVATE" |
| `fuelling_date` | `date` |  | The timestamp of the fuel transaction. |  |
| `fuelling_station` | string |  | The input name of the specific fuel station where the event took place. | "Park Service Station" |
| `is_tank_full` | boolean | null |  | Indicates if the vehicle's fuel tank is full. | true |
| `fuel_card` | string | null |  | The card number used in the transaction. Registration is required with this field. Card number must be registered in Contract Storage -> Fuel Cards. | "1234567890" |
| `odometer` | integer | null |  | The input odometer value for the vehicle at the transaction time. | 100101 |

## `mifleet-fuel-response` {#schema-mifleet-fuel-response}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `id` | integer |  | Unique identifier for the fuel entry. | 12345 |
| `document_number` | string |  | Unique reference number for the transaction document. | "ACCIDENT-123456" |
| `document_type` | string |  | The document type. Valid values are "DOCUMENT_TYPE_INVOICE", "DOCUMENT_TYPE_CREDIT_NOTE", "DOCUMENT_TYPE_DEBIT_NOTE", or "DOCUMENT_TYPE_RECEIPT". | "DOCUMENT_TYPE_INVOICE" |
| `document_status` | string |  | The document status. Valid values are "DOCUMENT_STATUS_PENDING", "DOCUMENT_STATUS_VALIDATED", "DOCUMENT_STATUS_OVERDUE_PAYMENT", "DOCUMENT_STATUS_PAID", or "DOCUMENT_STATUS_CANCELLED". | "DOCUMENT_STATUS_PAID" |
| `supplier` | string |  | Supplier or service provider for the entry. | "ABC Towing" |
| `description` | string |  | Brief summary or description of the transaction. | "Service description" |
| `quantity` | number |  | Quantity of items or services. | 1 |
| `price` | number |  | Unit price of the item or service. | 100 |
| `net_value` | number |  | Net value before tax. | 100 |
| `tax_rate` | number |  | Tax rate for the transaction (e.g., 0.2 for 20%). | 0.15 |
| `total_value` | number |  | Total value including tax. | 115 |
| `driver` | string |  | Driver associated with the entry. | "John Doe" |
| `general_ledger_code` | number | null |  | The general ledger number for the transaction. | 100101 |
| `discount` | number | null |  | The discount value that applies. | 1.25 |
| `registration` | `registration` |  |  |  |
| `vehicle_deleted` | boolean |  | Indicates if the vehicle associated with the fuel entry has been deleted. | false |
| `fuel_transaction_type` | string |  | The fuel transaction type. Valid values are "FUEL_TRANSACTION_TYPE_PRIVATE" or "FUEL_TRANSACTION_TYPE_BUSINESS". Defaults to "FUEL_TRANSACTION_TYPE_BUSINESS" if not provided. | "FUEL_TRANSACTION_TYPE_PRIVATE" |
| `fuelling_date` | `date` |  | The timestamp of the fuel transaction. |  |
| `fuelling_station` | string | null |  | The input name of the specific fuel station where the event took place. | "Park Service Station" |
| `ct_fuel_station` | string | null |  | Fuel station system found closest to the transaction time | "Park Service Station" |
| `ct_fuel_station_latitude` | number | null |  | Latitude of the fuel station closest to the transaction time | 40.712776 |
| `ct_fuel_station_longitude` | number | null |  | Longitude of the fuel station closest to the transaction time | -74.005974 |
| `ct_latitude` | number | null |  | Latitude of the vehicle closest to the transaction time (system found) | 40.713776 |
| `ct_longitude` | number | null |  | Longitude of the vehicle closest to the transaction time (system found) | -74.004974 |
| `consumption` | number | null |  | Calculated consumption in L/100km since the last fuel event. | 8.5 |
| `fuel_validation_status` | string | null |  | Result status of the fuel validation process | "FUEL_VALIDATION_MANAGER_APPROVED" |
| `ct_odometer` | number | null |  | Odometer value for the vehicle closest to the transaction time (system found) | 100101 |
| `ct_quantity` | number | null |  | Amount of litres at the transaction time (system sensor found) | 50 |
| `is_tank_full` | boolean | null |  | Indicates if the vehicle's fuel tank is full. | true |
| `fuel_card` | string | null |  | The card number used in the transaction. Registration is required with this field. Card number must be registered in Contract Storage -> Fuel Cards. | "1234567890" |
| `odometer` | integer | null |  | The input odometer value for the vehicle at the transaction time. | 100101 |

## `mifleet-insurance-response` {#schema-mifleet-insurance-response}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `id` | integer |  | Unique identifier for each insurance entry, aiding in tracking and referencing. | 12345 |
| `vehicle_deleted` | boolean |  | Indicates whether the associated vehicle is deleted or not. | false |
| `document_number` | string |  | Unique reference number for the transaction document. | "ACCIDENT-123456" |
| `document_type` | string |  | The document type. Valid values are "DOCUMENT_TYPE_INVOICE", "DOCUMENT_TYPE_CREDIT_NOTE", "DOCUMENT_TYPE_DEBIT_NOTE", or "DOCUMENT_TYPE_RECEIPT". | "DOCUMENT_TYPE_INVOICE" |
| `document_status` | string |  | The document status. Valid values are "DOCUMENT_STATUS_PENDING", "DOCUMENT_STATUS_VALIDATED", "DOCUMENT_STATUS_OVERDUE_PAYMENT", "DOCUMENT_STATUS_PAID", or "DOCUMENT_STATUS_CANCELLED". | "DOCUMENT_STATUS_PAID" |
| `supplier` | string |  | Supplier or service provider for the entry. | "ABC Towing" |
| `description` | string |  | Brief summary or description of the transaction. | "Service description" |
| `quantity` | number |  | Quantity of items or services. | 1 |
| `price` | number |  | Unit price of the item or service. | 100 |
| `net_value` | number |  | Net value before tax. | 100 |
| `tax_rate` | number |  | Tax rate for the transaction (e.g., 0.2 for 20%). | 0.15 |
| `total_value` | number |  | Total value including tax. | 115 |
| `discount` | number |  | Discount applied to the insurance entry. | 5 |
| `registration` | `registration` |  |  |  |
| `general_ledger_code` | integer |  | Code used for categorizing this transaction in the general ledger. | 12345 |
| `insurance_date` | `date` |  | The date of the insurance transaction. |  |

## `mifleet-leasing-cost-response` {#schema-mifleet-leasing-cost-response}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `id` | integer |  | Unique identifier for each leasing cost entry, aiding in tracking and referencing. | 12345 |
| `vehicle_deleted` | boolean |  | Indicates whether the associated vehicle is deleted or not. | false |
| `document_number` | string |  | Unique reference number for the transaction document. | "ACCIDENT-123456" |
| `document_type` | string |  | The document type. Valid values are "DOCUMENT_TYPE_INVOICE", "DOCUMENT_TYPE_CREDIT_NOTE", "DOCUMENT_TYPE_DEBIT_NOTE", or "DOCUMENT_TYPE_RECEIPT". | "DOCUMENT_TYPE_INVOICE" |
| `document_status` | string |  | The document status. Valid values are "DOCUMENT_STATUS_PENDING", "DOCUMENT_STATUS_VALIDATED", "DOCUMENT_STATUS_OVERDUE_PAYMENT", "DOCUMENT_STATUS_PAID", or "DOCUMENT_STATUS_CANCELLED". | "DOCUMENT_STATUS_PAID" |
| `supplier` | string |  | Supplier or service provider for the entry. | "ABC Towing" |
| `description` | string |  | Brief summary or description of the transaction. | "Service description" |
| `quantity` | number |  | Quantity of items or services. | 1 |
| `price` | number |  | Unit price of the item or service. | 100 |
| `net_value` | number |  | Net value before tax. | 100 |
| `tax_rate` | number |  | Tax rate for the transaction (e.g., 0.2 for 20%). | 0.15 |
| `total_value` | number |  | Total value including tax. | 115 |
| `discount` | number |  | Discount applied to the insurance entry. | 5 |
| `registration` | `registration` |  |  |  |
| `general_ledger_code` | integer |  | Code used for categorizing this transaction in the general ledger. | 12345 |
| `leasing_cost_date` | `date` |  | The date of the leasing cost transaction. |  |
| `leasing_cost_type` | string |  | The type of leasing cost incurred. | "Trailer Lease" |

## `mifleet-maintenance` {#schema-mifleet-maintenance}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `maintenance_date` | `date` |  | The timestamp of the maintenance transaction. |  |
| `maintenance_type` | string |  | The type of maintenance performed. | "Cleaning" |
| `budget` | number |  | The budget set for the maintenance operation. | 150 |
| `vehicle_mmv` | string |  | The make/model/variant of the vehicle. | "ISUZU NQR 500 AMT F/C C/C" |
| `job_card_reference` | string |  | The job card reference for the maintenance operation. | "JOB1234" |
| `fleet_controller` | string |  | The fleet controller responsible for the maintenance operation. | "John Doe" |

## `mifleet-maintenance-response` {#schema-mifleet-maintenance-response}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `id` | integer |  | Unique identifier for the maintenance entry. | 12345 |
| `document_number` | string |  | Unique reference number for the transaction document. | "ACCIDENT-123456" |
| `document_type` | string |  | The document type. Valid values are "DOCUMENT_TYPE_INVOICE", "DOCUMENT_TYPE_CREDIT_NOTE", "DOCUMENT_TYPE_DEBIT_NOTE", or "DOCUMENT_TYPE_RECEIPT". | "DOCUMENT_TYPE_INVOICE" |
| `document_status` | string |  | The document status. Valid values are "DOCUMENT_STATUS_PENDING", "DOCUMENT_STATUS_VALIDATED", "DOCUMENT_STATUS_OVERDUE_PAYMENT", "DOCUMENT_STATUS_PAID", or "DOCUMENT_STATUS_CANCELLED". | "DOCUMENT_STATUS_PAID" |
| `supplier` | string |  | Supplier or service provider for the entry. | "ABC Towing" |
| `description` | string |  | Brief summary or description of the transaction. | "Service description" |
| `quantity` | number |  | Quantity of items or services. | 1 |
| `price` | number |  | Unit price of the item or service. | 100 |
| `net_value` | number |  | Net value before tax. | 100 |
| `tax_rate` | number |  | Tax rate for the transaction (e.g., 0.2 for 20%). | 0.15 |
| `total_value` | number |  | Total value including tax. | 115 |
| `driver` | string |  | Driver associated with the entry. | "John Doe" |
| `registration` | `registration` |  |  |  |
| `vehicle_deleted` | boolean |  | Indicates whether the vehicle associated with this transaction has been marked as deleted. | false |
| `general_ledger_code` | integer | null |  | General ledger code assigned to the transaction for accounting purposes. | 100101 |
| `discount` | number | null |  | Discount amount, if any, applied to the transaction. Can be null. | 10 |
| `odometer` | number | null |  | Recorded odometer reading of the vehicle at the time of maintenance, can be null. | 120000 |
| `maintenance_date` | `date` |  | The timestamp of the maintenance transaction. |  |
| `maintenance_type` | string |  | The type of maintenance performed. | "Cleaning" |
| `budget` | number |  | The budget set for the maintenance operation. | 150 |
| `vehicle_mmv` | string |  | The make/model/variant of the vehicle. | "ISUZU NQR 500 AMT F/C C/C" |
| `job_card_reference` | string |  | The job card reference for the maintenance operation. | "JOB1234" |
| `fleet_controller` | string |  | The fleet controller responsible for the maintenance operation. | "John Doe" |

## `mifleet-oil` {#schema-mifleet-oil}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `fill_in_date` | `date` |  | Date and time of the oil fill-in transaction. |  |
| `oil_type` | string |  | The type of oil used in the transaction. | "Motor Oil" |

## `mifleet-oil-response` {#schema-mifleet-oil-response}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `id` | integer |  | Unique identifier for the oil entry. | 12345 |
| `document_number` | string |  | Unique reference number for the transaction document. | "ACCIDENT-123456" |
| `document_type` | string |  | The document type. Valid values are "DOCUMENT_TYPE_INVOICE", "DOCUMENT_TYPE_CREDIT_NOTE", "DOCUMENT_TYPE_DEBIT_NOTE", or "DOCUMENT_TYPE_RECEIPT". | "DOCUMENT_TYPE_INVOICE" |
| `document_status` | string |  | The document status. Valid values are "DOCUMENT_STATUS_PENDING", "DOCUMENT_STATUS_VALIDATED", "DOCUMENT_STATUS_OVERDUE_PAYMENT", "DOCUMENT_STATUS_PAID", or "DOCUMENT_STATUS_CANCELLED". | "DOCUMENT_STATUS_PAID" |
| `supplier` | string |  | Supplier or service provider for the entry. | "ABC Towing" |
| `description` | string |  | Brief summary or description of the transaction. | "Service description" |
| `quantity` | number |  | Quantity of items or services. | 1 |
| `price` | number |  | Unit price of the item or service. | 100 |
| `net_value` | number |  | Net value before tax. | 100 |
| `tax_rate` | number |  | Tax rate for the transaction (e.g., 0.2 for 20%). | 0.15 |
| `total_value` | number |  | Total value including tax. | 115 |
| `registration` | `registration` |  |  |  |
| `vehicle_deleted` | boolean |  | Indicates if the vehicle associated with this entry has been deleted. | false |
| `general_ledger_code` | number | null |  | Unique code for categorizing the transaction in the general ledger. | 54321 |
| `discount` | number | null |  | Discount amount applied to the transaction. | 0.35 |
| `fill_in_date` | `date` |  | Date and time of the oil fill-in transaction. |  |
| `oil_type` | string |  | The type of oil used in the transaction. | "Motor Oil" |

## `mifleet-purchase` {#schema-mifleet-purchase}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `registration_date` | `date` |  | Date and time of the purchase registration. |  |
| `odometer_at_purchase` | integer |  | The odometer value of the vehicle at the time of purchase. | 15000 |
| `accessories_price` | number |  | The value of accessories or add-ons as part of the transaction. | 2500.75 |
| `administration_tax` | number |  | The value of administration tax as part of the transaction. | 150 |
| `registration_tax` | number |  | The value of registration tax as part of the transaction. | 300 |
| `depreciation_tax` | number |  | The value of depreciation tax as part of the transaction. | 200 |
| `residual_value` | number |  | The value of residual value of the transaction. | 5000 |
| `estimated_lifetime` | integer |  | The estimated lifetime/term of the transaction. | 120 |

## `mifleet-purchase-response` {#schema-mifleet-purchase-response}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `id` | integer |  | Unique identifier for the purchase entry. | 12345 |
| `document_number` | string |  | Unique reference number for the transaction document. | "ACCIDENT-123456" |
| `document_type` | string |  | The document type. Valid values are "DOCUMENT_TYPE_INVOICE", "DOCUMENT_TYPE_CREDIT_NOTE", "DOCUMENT_TYPE_DEBIT_NOTE", or "DOCUMENT_TYPE_RECEIPT". | "DOCUMENT_TYPE_INVOICE" |
| `document_status` | string |  | The document status. Valid values are "DOCUMENT_STATUS_PENDING", "DOCUMENT_STATUS_VALIDATED", "DOCUMENT_STATUS_OVERDUE_PAYMENT", "DOCUMENT_STATUS_PAID", or "DOCUMENT_STATUS_CANCELLED". | "DOCUMENT_STATUS_PAID" |
| `supplier` | string |  | Supplier or service provider for the entry. | "ABC Towing" |
| `description` | string |  | Brief summary or description of the transaction. | "Service description" |
| `quantity` | number |  | Quantity of items or services. | 1 |
| `price` | number |  | Unit price of the item or service. | 100 |
| `net_value` | number |  | Net value before tax. | 100 |
| `tax_rate` | number |  | Tax rate for the transaction (e.g., 0.2 for 20%). | 0.15 |
| `total_value` | number |  | Total value including tax. | 115 |
| `registration` | `registration` |  |  |  |
| `vehicle_deleted` | boolean |  | Indicates if the vehicle associated with the registration has been deleted. | false |
| `general_ledger_code` | number | null |  | Unique code for categorizing the transaction in the general ledger. | 54321 |
| `discount` | number | null |  | Discount amount applied to the transaction. | 0.35 |
| `registration_date` | `date` |  | Date and time of the purchase registration. |  |
| `odometer_at_purchase` | integer |  | The odometer value of the vehicle at the time of purchase. | 15000 |
| `accessories_price` | number |  | The value of accessories or add-ons as part of the transaction. | 2500.75 |
| `administration_tax` | number |  | The value of administration tax as part of the transaction. | 150 |
| `registration_tax` | number |  | The value of registration tax as part of the transaction. | 300 |
| `depreciation_tax` | number |  | The value of depreciation tax as part of the transaction. | 200 |
| `residual_value` | number |  | The value of residual value of the transaction. | 5000 |
| `estimated_lifetime` | integer |  | The estimated lifetime/term of the transaction. | 120 |

## `mifleet-rental-cost` {#schema-mifleet-rental-cost}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `rental_cost_date` | `date` |  | Date and time of the rental cost transaction. |  |
| `rental_cost_type` | string |  | Type of rental cost (e.g., short-term rental, long-term rental). | "Short-term rental" |

## `mifleet-rental-cost-response` {#schema-mifleet-rental-cost-response}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `id` | integer |  | Unique identifier for the rental cost entry. | 12345 |
| `document_number` | string |  | Unique reference number for the transaction document. | "ACCIDENT-123456" |
| `document_type` | string |  | The document type. Valid values are "DOCUMENT_TYPE_INVOICE", "DOCUMENT_TYPE_CREDIT_NOTE", "DOCUMENT_TYPE_DEBIT_NOTE", or "DOCUMENT_TYPE_RECEIPT". | "DOCUMENT_TYPE_INVOICE" |
| `document_status` | string |  | The document status. Valid values are "DOCUMENT_STATUS_PENDING", "DOCUMENT_STATUS_VALIDATED", "DOCUMENT_STATUS_OVERDUE_PAYMENT", "DOCUMENT_STATUS_PAID", or "DOCUMENT_STATUS_CANCELLED". | "DOCUMENT_STATUS_PAID" |
| `supplier` | string |  | Supplier or service provider for the entry. | "ABC Towing" |
| `description` | string |  | Brief summary or description of the transaction. | "Service description" |
| `quantity` | number |  | Quantity of items or services. | 1 |
| `price` | number |  | Unit price of the item or service. | 100 |
| `net_value` | number |  | Net value before tax. | 100 |
| `tax_rate` | number |  | Tax rate for the transaction (e.g., 0.2 for 20%). | 0.15 |
| `total_value` | number |  | Total value including tax. | 115 |
| `registration` | `registration` |  |  |  |
| `vehicle_deleted` | boolean |  | Indicates if the vehicle associated with the registration has been deleted. | false |
| `general_ledger_code` | number | null |  | Unique code for categorizing the transaction in the general ledger. | 54321 |
| `discount` | number | null |  | Discount amount applied to the transaction. | 0.35 |
| `rental_cost_date` | `date` |  | Date and time of the rental cost transaction. |  |
| `rental_cost_type` | string |  | Type of rental cost (e.g., short-term rental, long-term rental). | "Short-term rental" |

## `mifleet-tax` {#schema-mifleet-tax}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `begin_date` | `date` |  | The start date of the tax transaction period. |  |
| `expiration_date` | `date` |  | The end date of the tax transaction period. |  |

## `mifleet-tax-response` {#schema-mifleet-tax-response}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `id` | integer |  | Unique identifier for the tax entry. | 12345 |
| `document_number` | string |  | Unique reference number for the transaction document. | "ACCIDENT-123456" |
| `document_type` | string |  | The document type. Valid values are "DOCUMENT_TYPE_INVOICE", "DOCUMENT_TYPE_CREDIT_NOTE", "DOCUMENT_TYPE_DEBIT_NOTE", or "DOCUMENT_TYPE_RECEIPT". | "DOCUMENT_TYPE_INVOICE" |
| `document_status` | string |  | The document status. Valid values are "DOCUMENT_STATUS_PENDING", "DOCUMENT_STATUS_VALIDATED", "DOCUMENT_STATUS_OVERDUE_PAYMENT", "DOCUMENT_STATUS_PAID", or "DOCUMENT_STATUS_CANCELLED". | "DOCUMENT_STATUS_PAID" |
| `supplier` | string |  | Supplier or service provider for the entry. | "ABC Towing" |
| `description` | string |  | Brief summary or description of the transaction. | "Service description" |
| `quantity` | number |  | Quantity of items or services. | 1 |
| `price` | number |  | Unit price of the item or service. | 100 |
| `net_value` | number |  | Net value before tax. | 100 |
| `tax_rate` | number |  | Tax rate for the transaction (e.g., 0.2 for 20%). | 0.15 |
| `total_value` | number |  | Total value including tax. | 115 |
| `registration` | `registration` |  |  |  |
| `vehicle_deleted` | boolean |  | Indicates if the vehicle associated with the entry has been deleted. | false |
| `general_ledger_code` | number | null |  | Unique code for categorizing the transaction in the general ledger. | 54321 |
| `discount` | number | null |  | Discount amount applied to the transaction. | 0.35 |
| `begin_date` | `date` |  | The start date of the tax transaction period. |  |
| `expiration_date` | `date` |  | The end date of the tax transaction period. |  |

## `mifleet-toll` {#schema-mifleet-toll}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `toll_date` | `date` |  | The exact date and time when the toll transaction occurred. |  |
| `passage_name` | string |  | The specific name or identifier of the toll passage or station. | "Main Street Toll Booth" |

## `mifleet-toll-response` {#schema-mifleet-toll-response}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `id` | integer |  | A unique identifier for each toll transaction, facilitating easy tracking and reference. | 12345 |
| `document_number` | string |  | Unique reference number for the transaction document. | "ACCIDENT-123456" |
| `document_type` | string |  | The document type. Valid values are "DOCUMENT_TYPE_INVOICE", "DOCUMENT_TYPE_CREDIT_NOTE", "DOCUMENT_TYPE_DEBIT_NOTE", or "DOCUMENT_TYPE_RECEIPT". | "DOCUMENT_TYPE_INVOICE" |
| `document_status` | string |  | The document status. Valid values are "DOCUMENT_STATUS_PENDING", "DOCUMENT_STATUS_VALIDATED", "DOCUMENT_STATUS_OVERDUE_PAYMENT", "DOCUMENT_STATUS_PAID", or "DOCUMENT_STATUS_CANCELLED". | "DOCUMENT_STATUS_PAID" |
| `supplier` | string |  | Supplier or service provider for the entry. | "ABC Towing" |
| `description` | string |  | Brief summary or description of the transaction. | "Service description" |
| `quantity` | number |  | Quantity of items or services. | 1 |
| `price` | number |  | Unit price of the item or service. | 100 |
| `net_value` | number |  | Net value before tax. | 100 |
| `tax_rate` | number |  | Tax rate for the transaction (e.g., 0.2 for 20%). | 0.15 |
| `total_value` | number |  | Total value including tax. | 115 |
| `driver` | string |  | Driver associated with the entry. | "John Doe" |
| `registration` | `registration` |  |  |  |
| `vehicle_deleted` | boolean |  | If the vehicle is deleted or not. | false |
| `ct_toll_station` | string | null |  | The toll station system found closest to the transaction time. | "Downtown Toll Plaza" |
| `ct_toll_station_latitude` | number | null |  | The latitude coordinate of the toll station. | 40.7128 |
| `ct_toll_station_longitude` | number | null |  | The longitude coordinate of the toll station. | -74.006 |
| `ct_latitude` | number | null |  | The system found latitude for the vehicle closest to the transaction time. | 40.713 |
| `ct_longitude` | number | null |  | The system found longitude for the vehicle closest to the transaction time. | -74.0059 |
| `ct_odometer` | number | null |  | The odometer value system found closest to the transaction time. | 15000 |
| `general_ledger_code` | integer | null |  | The general ledger code. | 54321 |
| `discount` | number | null |  | The discount amount, if any, applied to the transaction. | 0.35 |
| `toll_validation_status` | string | null |  | The result status of the validation process. | "TOLL_VALIDATION_MANAGER_APPROVED" |
| `toll_date` | `date` |  | The exact date and time when the toll transaction occurred. |  |
| `passage_name` | string |  | The specific name or identifier of the toll passage or station. | "Main Street Toll Booth" |

## `mifleet-tyre-response` {#schema-mifleet-tyre-response}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `id` | integer |  | Unique identifier for the tyre entry. | 12345 |
| `document_number` | string |  | Unique reference number for the transaction document. | "ACCIDENT-123456" |
| `document_type` | string |  | The document type. Valid values are "DOCUMENT_TYPE_INVOICE", "DOCUMENT_TYPE_CREDIT_NOTE", "DOCUMENT_TYPE_DEBIT_NOTE", or "DOCUMENT_TYPE_RECEIPT". | "DOCUMENT_TYPE_INVOICE" |
| `document_status` | string |  | The document status. Valid values are "DOCUMENT_STATUS_PENDING", "DOCUMENT_STATUS_VALIDATED", "DOCUMENT_STATUS_OVERDUE_PAYMENT", "DOCUMENT_STATUS_PAID", or "DOCUMENT_STATUS_CANCELLED". | "DOCUMENT_STATUS_PAID" |
| `supplier` | string |  | Supplier or service provider for the entry. | "ABC Towing" |
| `description` | string |  | Brief summary or description of the transaction. | "Service description" |
| `quantity` | number |  | Quantity of items or services. | 1 |
| `price` | number |  | Unit price of the item or service. | 100 |
| `net_value` | number |  | Net value before tax. | 100 |
| `tax_rate` | number |  | Tax rate for the transaction (e.g., 0.2 for 20%). | 0.15 |
| `total_value` | number |  | Total value including tax. | 115 |
| `driver` | string |  | Driver associated with the entry. | "John Doe" |
| `registration` | `registration` |  |  |  |
| `vehicle_deleted` | boolean |  | Indicates if the vehicle associated with the entry has been deleted. | false |
| `discount` | number | null |  | Discount amount applied to the transaction. | 0.35 |
| `general_ledger_code` | number | null |  | Unique code for categorizing the transaction in the general ledger. | 54321 |
| `tyre_date` | `date` |  | Date and time of the transaction. |  |
| `tyre_operation` | string |  | Operation performed in this transaction. | "Tyre Rotation" |
| `odometer` | integer |  | The vehicle odometer value at the time of the transaction. | 101001 |
| `tyres` | array of object |  | An optional array of each tyre specifically impacted by this transaction. |  |

## `mifleet-vehicle-license-response` {#schema-mifleet-vehicle-license-response}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `id` | integer |  | Unique identifier for each transaction, aiding in tracking and referencing. | 78901 |
| `document_number` | string |  | Unique reference number for the transaction document. | "ACCIDENT-123456" |
| `document_type` | string |  | The document type. Valid values are "DOCUMENT_TYPE_INVOICE", "DOCUMENT_TYPE_CREDIT_NOTE", "DOCUMENT_TYPE_DEBIT_NOTE", or "DOCUMENT_TYPE_RECEIPT". | "DOCUMENT_TYPE_INVOICE" |
| `document_status` | string |  | The document status. Valid values are "DOCUMENT_STATUS_PENDING", "DOCUMENT_STATUS_VALIDATED", "DOCUMENT_STATUS_OVERDUE_PAYMENT", "DOCUMENT_STATUS_PAID", or "DOCUMENT_STATUS_CANCELLED". | "DOCUMENT_STATUS_PAID" |
| `supplier` | string |  | Supplier or service provider for the entry. | "ABC Towing" |
| `description` | string |  | Brief summary or description of the transaction. | "Service description" |
| `quantity` | number |  | Quantity of items or services. | 1 |
| `price` | number |  | Unit price of the item or service. | 100 |
| `net_value` | number |  | Net value before tax. | 100 |
| `tax_rate` | number |  | Tax rate for the transaction (e.g., 0.2 for 20%). | 0.15 |
| `total_value` | number |  | Total value including tax. | 115 |
| `registration` | `registration` |  |  |  |
| `vehicle_deleted` | boolean | null |  | Indicates whether the associated vehicle is deleted or not | true |
| `discount` | number | null |  | Discount amount applied to the transaction. | 0.35 |
| `general_ledger_code` | integer | null |  | Code used for categorizing this transaction in the general ledger. | 54321 |
| `vehicle_license_type` | string |  | The type of license for this transaction. | "Trailer License" |
| `begin_odometer` | integer | null |  | The start odometer of the license period. | 100000 |
| `end_odometer` | integer | null |  | The end odometer of the license period. | 120000 |
| `begin_date` | `date` |  | The start date of the license period. |  |
| `expiration_date` | `date` |  | The end date of the license period. |  |

## `page` {#schema-page}

Page number for pagination.

_Type: integer_


**Example:**

```json
1
```


## `pagination` {#schema-pagination}

The metadata such as pagination, current_page and more.

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `from` | integer | **required** |  | 1 |
| `to` | integer | **required** |  | 15 |
| `current_page` | integer | **required** |  | 1 |
| `per_page` | integer | **required** |  | 15 |
| `last_page` | integer | **required** |  | 3 |
| `total` | integer | **required** |  | 45 |

## `phone-code` {#schema-phone-code}

Phone country code

_Type: string_


**Example:**

```json
65
```


## `plan-id` {#schema-plan-id}

The ID of the delivery plan

_Type: integer_


**Example:**

```json
12345
```


## `plan-state-id` {#schema-plan-state-id}

Plan state identifier  

| plan_state_id | Description |
| --- | --- |
| 1 | New |
| 2 | Deleted |
| 3 | Confirmed |

_Type: integer_


**Example:**

```json
3
```


## `priority-id` {#schema-priority-id}

The priority\_id is the type of alert you want to prioritize over other alerts.  

* 1 represents High
* 2 represents Medium
* 3 represents Low

**Required only for Alert Center (contact_type_id = 4)**

_Type: integer_


**Example:**

```json
1
```


## `registration` {#schema-registration}

The registration number of the vehicle.

_Type: string_


**Example:**

```json
ABX123
```


## `registrations` {#schema-registrations}

A list of vehicle registrations for which the alert must be set. If no registrations are provided, the alert will apply to all vehicles in the fleet.

_Type: array_


**Example:**

```json
[
  "ABC1234",
  "ABC5678"
]
```


## `schedule-type-id` {#schema-schedule-type-id}

Schedule type identifier  

| schedule\_type\_id | Description |
| --- | --- |
| 1 | As Soon As Possible |
| 2 | Scheduled |
| 3 | Unscheduled |

_Type: integer_


**Example:**

```json
1
```


## `special-equipment` {#schema-special-equipment}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `equipment_id` | integer |  | The equipment ID |  |
| `equipment_name` | string |  | The equipment name |  |

## `stop-status-id` {#schema-stop-status-id}

Delivery stop status ID  

| stop\_status\_id | Description |
| --- | --- |
| 1 | Created |
| 2 | Started |
| 3 | Arrived |
| 4 | Completed |
| 5 | Rejected |

_Type: integer_


## `stop-todo-status-id` {#schema-stop-todo-status-id}

Todo status ID  

| todo\_status\_id | Description | Valid for todo\_type\_id |
| --- | --- | --- |
| 1 | Customer Not Show | 1 |
| 2 | Refuse to Sign | 1 |
| 3 | Others | 1 |
| 4 | No Response | 2 |
| 5 | No Person at Home | 2 |
| 6 | Others | 2 |
| 7 | Technical Issue | 3 |
| 8 | Others | 3 |
| 9 | Completed OK | 1, 2, 3 |

_Type: integer_


## `stop-todo-type-id` {#schema-stop-todo-type-id}

Todo type ID  

| todo\_type\_id | Description |
| --- | --- |
| 1 | Get Signature |
| 2 | Take Photo (POD) |
| 3 | Scan to Attach |
| 5 | Note |

_Type: integer_


## `stop-type-id` {#schema-stop-type-id}

Delivery stop type ID  

| stop\_type\_id | Description |
| --- | --- |
| 1 | Pickup |
| 2 | Delivery |
| 3 | Dropoff |

_Type: integer_


## `subuser-id` {#schema-subuser-id}

Sub-user identifier associated with the geofence group

_Type: integer | null_


**Example:**

```json
54321
```


## `terminal-id` {#schema-terminal-id}

The id of the terminal installed in the vehicle.

_Type: integer_


**Example:**

```json
123456789
```


## `terminal-serial` {#schema-terminal-serial}

The serial of the Cartrack terminal.

_Type: string_


**Example:**

```json
TS123456789
```


## `timestamp` {#schema-timestamp}

Timestamp

_Type: string_


**Example:**

```json
2023-01-01 12:00:00+00:00
```


## `timestamp-with-offset` {#schema-timestamp-with-offset}

Timestamp with timezone offset

_Type: string_


**Example:**

```json
2023-01-01 12:00:00+08:00
```


## `user-id` {#schema-user-id}

User identifier

_Type: integer_


**Example:**

```json
227020
```


## `vehicle-event-base` {#schema-vehicle-event-base}

_Type: object_

| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `event_id` | integer |  | The event id | 123456 |
| `vehicle_id` | `vehicle-id` |  |  |  |
| `registration` | `registration` |  |  |  |
| `chassis_number` | `chassis-number` |  |  |  |
| `terminal_event_type_id` | integer |  | The event type id | 2 |
| `event_description` | string |  | The event description (event name). For the full list of supported vehicle event types and their meanings, refer to https://developer.cartrack.com/docs/fleet-api-general/services/vehicles-events-services#vehicle-event-types | "IGNITION_ON" |
| `longitude` | number | null |  | The longitude of the event | 103.889653 |
| `latitude` | number | null |  | The latitude of the event | 1.320227 |
| `linear_g` | number | null |  | The linear G force of the event | 0 |
| `lateral_g` | number | null |  | The lateral G force of the event | 0 |
| `altitude` | integer | null |  | The vehicle's altitude, in meters | 152 |
| `odometer` | integer | null |  | The vehicle's odometer, in meters. | 1000 |
| `clock` | integer | null |  | The vehicle's clock reading, in minutes. The clock value is the total time of engine ON since the Cartrack tracker was first installed. | 100500 |
| `bearing` | integer | null |  | The vehicle's bearing (degree 0-360) | 100 |
| `ignition` | boolean |  | The ignition status | true |
| `speed` | integer | null |  | The speed of the vehicle's in km/h | 60 |
| `road_speed` | integer | null |  | The speed limit of the road where the vehicle is, in km/h | 70 |
| `rpm` | integer | null |  | The vehicle's revolution per minute | 764 |
| `road_speeding` | boolean | null |  | The state if the vehicle's speed exceeded the road speed, in km/h | true |
| `temp1` | number | null |  | The vehicle's temperature sensor 1 | 0 |
| `temp2` | number | null |  | The vehicle's temperature sensor 2 | 0 |
| `temp3` | number | null |  | The vehicle's temperature sensor 3 | 0 |
| `temp4` | number | null |  | The vehicle's temperature sensor 4 | 0 |
| `analog_0` | integer | null |  | The vehicle's analog-to-digital converter signal (channel 0) | 0 |
| `analog_1` | integer | null |  | The vehicle's analog-to-digital converter signal (channel 1) | 0 |
| `analog_2` | integer | null |  | The vehicle's analog-to-digital converter signal (channel 2) | 0 |
| `adc0` | number | null |  | The vehicle's analog-to-digital converter signal (channel 0) expressed in voltage | 0 |
| `adc1` | number | null |  | The vehicle's analog-to-digital converter signal (channel 1) expressed in voltage | 0 |
| `adc2` | number | null |  | The vehicle's analog-to-digital converter signal (channel 2) expressed in voltage | 0 |
| `position_description_id` | integer |  | The position description id | 123456789 |
| `position_description` | string | null |  | Human-readable address derived from the vehicle's latitude and longitude (reverse geocoded). | "Tanjong Pagar, Singapore" |
| `input_state` | integer | null |  | The vehicle's first input state. A summation of can bus bit event in decimal place | 1601 |
| `input_state2` | integer | null |  | The vehicle's second input state. A summation of can bus bit event in decimal place | -2147483648 |
| `input_state3` | integer | null |  | The vehicle's third input state. A summation of can bus bit event in decimal place | -1 |
| `output_state` | number | null |  | The vehicle's output state value | 0 |
| `vext` | number |  | The vehicle's external battery voltage | 4.04 |
| `vgsm` | number |  | The vgsm represents the voltage of the Cartrack tracker's internal backup battery. If the tracker does not have a backup battery connected, it will return 0 (0V). | 4.13 |
| `dynamic1` | integer | null |  | The vehicle's first dynamic value. A range of values that can be set from the can bus data, without a placeholder | 1 |
| `dynamic2` | integer | null |  | The vehicle's second dynamic value. A range of values that can be set from the can bus data, without a placeholder |  |
| `dynamic3` | integer | null |  | The vehicle's third dynamic value. A range of values that can be set from the can bus data, without a placeholder |  |
| `dynamic4` | integer | null |  | The vehicle's fourth dynamic value. A range of values that can be set from the can bus data, without a placeholder |  |
| `battery_percentage_left` | integer | null |  | The vehicle's battery percentage left. A value in percentage for electric vehicles | 100 |
| `event_ts` | `timestamp` |  | The event date and time | "2022-01-28 16:48:03+02" |
| `received_ts` | `timestamp` |  | The received date and time | "2022-01-28 16:48:03+02" |
| `z_accel` | number | null |  | Z-Axis: This axis represents depth or forward/backward movements, corresponding to forward and backward or inward and outward movements. | 0.09 |
| `y_accel` | number | null |  | Y-Axis: This axis represents vertical movements, corresponding to up and down directions. | 0.02 |
| `x_accel` | number | null |  | X-Axis: This axis represents horizontal movements, corresponding to left and right directions. | 0.02 |
| `gps_fix_type` | integer | null |  | GPS fix type:  * 0 indicates no satellite fix. * 1 indicates an insufficient number of satellites. * 2 indicates limited number of satellites. * 3 indicates excellent satellite coverage. | 3 |
| `unit_temp` | number | null |  | The vehicle's unit temperature | 36.5 |
| `water_temp` | number | null |  | The vehicle's water temperature | 90 |
| `oil_temp` | number | null |  | The vehicle's oil temperature | 85 |
| `oil_pressure` | number | null |  | The vehicle's oil pressure | 30 |
| `terminal_id` | `terminal-id` |  |  |  |
| `user_id` | `id` |  | The user ID | 111101 |
| `driver_id` | string | null |  | The driver's uuid | "98547217-d5b7-11ec-b1fb-a4bf016cd6b2" |

## `vehicle-id` {#schema-vehicle-id}

Vehicle identification number

_Type: integer_


**Example:**

```json
654321
```

