// Copyright 2014-present, Apstra, Inc. All rights reserved.
//
// This source code is licensed under End User License Agreement found in the
// LICENSE file at http://www.apstra.com/community/eula

package aos

import (
	"io"
	"io/ioutil"
	"log"
	"net"
	"bytes"
	"strings"
	"reflect"
	"fmt"
	"time"
	"encoding/binary"
	"github.com/golang/protobuf/proto"
	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/plugins/inputs"
	"github.com/influxdata/telegraf/plugins/inputs/aos/aos_streaming"
	"github.com/influxdata/telegraf/plugins/inputs/aos/restapi"
)

// ----------------------------------------------------------------
// streamAos "Class"
// ----------------------------------------------------------------
type streamAos struct {
	net.Listener
	*Aos

	// connections    map[string]net.Conn
	// connectionsMtx sync.Mutex
}

func (ssl *streamAos) listen() {

	for {
		conn, err := ssl.Listener.Accept()
		if err != nil {
			log.Printf("W! Accepting Conn: " + err.Error())
			continue
		}

		go ssl.msgReader(conn)
	}

}

func (ssl *streamAos) extractEventData(eventType string, tags map[string]string, eventData interface{}) {

	myEventDataValue := reflect.ValueOf(eventData).Elem()
	myEventDataType := myEventDataValue.Type()
	propDataType := proto.GetProperties(myEventDataType)

	serie := "event_" + eventType
	fields := make(map[string]interface{})

	fields["event"] = 1

	for i := 0; i < myEventDataValue.NumField(); i++ {
		myField := myEventDataValue.Field(i)
		field_name := propDataType.Prop[i].OrigName

		// Skip field with XXX_
		if strings.Contains(field_name, "XXX_") {	continue }

		if propDataType.Prop[i].Enum != "" {
			field_value := fmt.Sprintf("%v", myField.Elem().Interface().(fmt.Stringer).String() )
			tags[field_name] = field_value
		} else {
			field_value := fmt.Sprintf("%v", reflect.Indirect(myField).Interface() )
			tags[field_name] = field_value
		}
	}

	ssl.Aos.Accumulator.AddFields(serie, fields, tags)
}

func (ssl *streamAos) extractAlertData(alertType string, tags map[string]string, alertData interface{}, raised bool) {

	myAlertDataValue := reflect.ValueOf(alertData).Elem()
	myAlertDataType := myAlertDataValue.Type()
	propDataType := proto.GetProperties(myAlertDataType)

	serie := "alert_" + strings.Replace(alertType, "_alert", "", -1)
	fields := make(map[string]interface{})

	if raised {
		fields["status"] = 1
	} else {
		fields["status"] = 0
	}

	for i := 0; i < myAlertDataValue.NumField(); i++ {
		myField := myAlertDataValue.Field(i)
		field_name := propDataType.Prop[i].OrigName

		// Skip field with XXX_
		if strings.Contains(field_name, "XXX_") {	continue }

		if propDataType.Prop[i].Enum != "" {
			field_value := fmt.Sprintf("%v", myField.Elem().Interface().(fmt.Stringer).String() )
			tags[field_name] = field_value
		} else {
			field_value := fmt.Sprintf("%v", reflect.Indirect(myField).Interface() )
			tags[field_name] = field_value
		}
	}
	ssl.Aos.Accumulator.AddFields(serie, fields, tags)
}

func (ssl *streamAos) GetTags(deviceKey string) map[string]string {

	tags := make(map[string]string)

	// search for :: in string and split if found
	if strings.Contains(deviceKey, "::") {
		devInt := strings.Split(deviceKey,  "::")
		deviceKey = devInt[0]
		tags["interface"] = devInt[1]
	}

	tags["device_key"] = deviceKey

	system := ssl.Aos.api.GetSystemByKey(deviceKey)

	if system != nil {
		if system.Blueprint.Role != "" {
			tags["role"] = system.Blueprint.Role
		}

		if system.Status.BlueprintId != "" {
			tags["blueprint"] = ssl.Aos.api.Blueprints[system.Status.BlueprintId].Name
		}

		if system.Blueprint.Name != "" {
			tags["device_name"] = system.Blueprint.Name
			tags["device"] = system.Blueprint.Name
		} else {
			tags["device"] = deviceKey
		}
	} else {
		tags["device"] = deviceKey
	}

	return tags
}

func (ssl *streamAos) msgReader(r io.Reader) {
	var msgSize uint16

	log.Printf("D! New TCP Session Opened .. ")

	for {
		sizeReader := io.LimitReader(r, 2)
		sizeBuf, err := ioutil.ReadAll(sizeReader)

		if err != nil {
			log.Printf("W! Reading Size failed: ", err)
			return
		}

		err = binary.Read(
			bytes.NewReader(sizeBuf),
			binary.BigEndian,
			&msgSize)

		if err != nil {
			log.Printf("W! binary.Read failed: ", err)
			return
		}

		msgReader := io.LimitReader(r, int64(msgSize))
		msgBuf, err := ioutil.ReadAll(msgReader)

		if err != nil {
			log.Printf("W! Reading message failed: ", err)
		}

		// Create new aos_streaming.AosMessage and deserialize protobuf
		newMsg := new(aos_streaming.AosMessage)
		err = proto.Unmarshal(msgBuf, newMsg)

		if err != nil {
			log.Printf("W! Error unmarshaling: ", err)
		}

		// ----------------------------------------------------------------
		// Extract all Types of data
		// ----------------------------------------------------------------
		newPerfMonData := newMsg.GetPerfMon()
		newEvent := newMsg.GetEvent()
		newAlert := newMsg.GetAlert()

		// ----------------------------------------------------------------
		// Extract Timestamp and Device Name
		// ----------------------------------------------------------------
		// timeStamp := time.Unix(newMsg.GetTimestamp(), 0)
		originName := newMsg.GetOriginName()

		if newPerfMonData != nil {

			newIntCounter := newPerfMonData.GetInterfaceCounters()
			newResourceCounter := newPerfMonData.GetSystemResourceCounters()

			// ----------------------------------------------------------------
			// Interface Counters
			// ----------------------------------------------------------------
			if newIntCounter != nil {

				// Extract device name from Interface name
				// s := strings.Split(originName, "::")
				// devName, devInt := s[0], s[1]

				// Prepare value. type and property
				myValue := reflect.ValueOf(newIntCounter).Elem()
				myType := myValue.Type()
				propType := proto.GetProperties(myType)

				serie := "interface_counters"
				fields := make(map[string]interface{})
				tags := ssl.GetTags( originName )

				// tags["interface"] = devInt

				for i := 0; i < myValue.NumField(); i++ {

						myField := myValue.Field(i)
						field_name := propType.Prop[i].OrigName

						// Skip field with XXX_
						if strings.Contains(field_name, "XXX_") {	continue	}

						fields[propType.Prop[i].OrigName] = reflect.Indirect(myField).Interface()
				}

				ssl.Aos.Accumulator.AddFields(serie, fields, tags)
			}

			// ----------------------------------------------------------------
			// Resource Counters
			// ----------------------------------------------------------------
			if newResourceCounter != nil {

				systemInfo := newResourceCounter.GetSystemInfo()
				processInfo := newResourceCounter.GetProcessInfo()
				fileInfo := newResourceCounter.GetFileInfo()

				if systemInfo != nil {

						// Prepare value. type and property
						myValue := reflect.ValueOf(systemInfo).Elem()
					  myType := myValue.Type()
						propType := proto.GetProperties(myType)

						serie := "system_info"
						fields := make(map[string]interface{})
						tags := ssl.GetTags( originName )

						for i := 0; i < myValue.NumField(); i++ {
							myField := myValue.Field(i)
							field_name := propType.Prop[i].OrigName

							// Skip field with XXX_
							if strings.Contains(field_name, "XXX_") {	continue }

							fields[field_name] = reflect.Indirect(myField).Interface()
						}

						ssl.Aos.Accumulator.AddFields(serie, fields, tags)
				}

				if processInfo != nil {

						for _, p := range processInfo {

							// Prepare value. type and property
							myValue := reflect.ValueOf(p).Elem()
							myType := myValue.Type()
							propType := proto.GetProperties(myType)

							// Get Process Name

							process_name := p.ProcessName

							serie := "process_info"
							fields := make(map[string]interface{})
							tags := ssl.GetTags( originName )

							tags["process_name"] = *process_name

							for i := 0; i < myValue.NumField(); i++ {
								myField := myValue.Field(i)
								field_name := propType.Prop[i].OrigName

								// Skip field with XXX_ and process_name
								if strings.Contains(field_name, "XXX_") {	continue }
								if strings.Contains(field_name, "process_name") {	continue }

								fields[field_name] = reflect.Indirect(myField).Interface()
							}

							ssl.Aos.Accumulator.AddFields(serie, fields, tags)
				    }
				}

				if fileInfo != nil {

					// Prepare value. type and property
					for _, f := range fileInfo {
						file_name := f.FileName
						file_size := f.FileSize

						serie := "file_info"
						fields := make(map[string]interface{})
						tags := ssl.GetTags( originName )

						tags["file_name"] = *file_name
						fields["size"] = *file_size

						ssl.Aos.Accumulator.AddFields(serie, fields, tags)
					}
				}
			}
		}

		if newEvent != nil {

			// ----------------------------------------------------------------
			// Collect all type of events
			// ----------------------------------------------------------------
			myEventValue := reflect.ValueOf(newEvent.Data).Elem()
			myEventType := myEventValue.Type()
			propType := proto.GetProperties(myEventType)

			eventTypeName := propType.Prop[0].OrigName

			tags := ssl.GetTags( originName )

			switch eventTypeName {
			case "device_state":
					myEventData := newEvent.GetDeviceState()
					ssl.extractEventData( eventTypeName, tags, myEventData)
			case "streaming":
					myEventData := newEvent.GetStreaming()
					ssl.extractEventData( eventTypeName, tags, myEventData)
			case "cable_peer":
					myEventData := newEvent.GetCablePeer()
					ssl.extractEventData( eventTypeName, tags, myEventData)
			case "bgp_neighbor":
					myEventData := newEvent.GetBgpNeighbor()
					ssl.extractEventData( eventTypeName, tags, myEventData)
			case "link_status":
					myEventData := newEvent.GetLinkStatus()
					ssl.extractEventData( eventTypeName, tags, myEventData)
			case "traffic":
					myEventData := newEvent.GetTraffic()
					ssl.extractEventData( eventTypeName, tags, myEventData)
			case "mac_state":
					myEventData := newEvent.GetMacState()
					ssl.extractEventData( eventTypeName, tags, myEventData)
			case "arp_state":
					myEventData := newEvent.GetArpState()
					ssl.extractEventData( eventTypeName, tags, myEventData)
			case "lag_state":
					myEventData := newEvent.GetLagState()
					ssl.extractEventData( eventTypeName, tags, myEventData)
			case "mlag_state":
					myEventData := newEvent.GetMlagState()
					ssl.extractEventData( eventTypeName, tags, myEventData)

			default:
				log.Printf("W! Event Type - %s, not supported yet", eventTypeName)
			}
		}

		if newAlert != nil {

			myAlertValue := reflect.ValueOf(newAlert.Data).Elem()
			myAlertType := myAlertValue.Type()
			propAlertType := proto.GetProperties(myAlertType)

			alertTypeName := propAlertType.Prop[0].OrigName

			tags := ssl.GetTags( originName )

			tags["severity"] = fmt.Sprintf("%v", newAlert.Severity)
			// tags["first_seen"] = fmt.Sprintf("%v", newAlert.FirstSeen)

			raise := *newAlert.Raised

			switch alertTypeName {
			case "config_deviation_alert":
					myAlertData := newAlert.GetConfigDeviationAlert()
					ssl.extractAlertData(alertTypeName, tags, myAlertData, raise)
			case "streaming_alert":
					myAlertData := newAlert.GetStreamingAlert()
					ssl.extractAlertData(alertTypeName, tags, myAlertData, raise)
			case "cable_peer_mismatch_alert":
					myAlertData := newAlert.GetCablePeerMismatchAlert()
					ssl.extractAlertData(alertTypeName, tags, myAlertData, raise)
			case "bgp_neighbor_mismatch_alert":
					myAlertData := newAlert.GetBgpNeighborMismatchAlert()
					ssl.extractAlertData(alertTypeName, tags, myAlertData, raise)
			case "interface_link_status_mismatch_alert":
					myAlertData := newAlert.GetInterfaceLinkStatusMismatchAlert()
					ssl.extractAlertData(alertTypeName, tags, myAlertData, raise)
			case "hostname_alert":
					myAlertData := newAlert.GetHostnameAlert()
					ssl.extractAlertData(alertTypeName, tags, myAlertData, raise)
			case "route_alert":
					myAlertData := newAlert.GetRouteAlert()
					ssl.extractAlertData(alertTypeName, tags, myAlertData, raise)
			case "liveness_alert":
					myAlertData := newAlert.GetLivenessAlert()
					ssl.extractAlertData(alertTypeName, tags, myAlertData, raise)
			case "deployment_alert":
					myAlertData := newAlert.GetDeploymentAlert()
					ssl.extractAlertData(alertTypeName, tags, myAlertData, raise)
			case "blueprint_rendering_alert":
					myAlertData := newAlert.GetBlueprintRenderingAlert()
					ssl.extractAlertData(alertTypeName, tags, myAlertData, raise)
			case "counters_alert":
					myAlertData := newAlert.GetCountersAlert()
					ssl.extractAlertData(alertTypeName, tags, myAlertData, raise)
			case "mac_alert":
					myAlertData := newAlert.GetMacAlert()
					ssl.extractAlertData(alertTypeName, tags, myAlertData, raise)
			case "arp_alert":
					myAlertData := newAlert.GetArpAlert()
					ssl.extractAlertData(alertTypeName, tags, myAlertData, raise)
			case "headroom_alert":
					myAlertData := newAlert.GetHeadroomAlert()
					ssl.extractAlertData(alertTypeName, tags, myAlertData, raise)
			case "lag_alert":
					myAlertData := newAlert.GetLagAlert()
					ssl.extractAlertData(alertTypeName, tags, myAlertData, raise)
			case "mlag_alert":
					myAlertData := newAlert.GetMlagAlert()
					ssl.extractAlertData(alertTypeName, tags, myAlertData, raise)
			case "test_alert":
					myAlertData := newAlert.GetTestAlert()
					ssl.extractAlertData(alertTypeName, tags, myAlertData, raise)

			default:
					log.Printf("W! Alert Type - %s, Not Supported Yet", alertTypeName )
			}
		}
	}
}

// ----------------------------------------------------------------
// Aos "Class"
// ----------------------------------------------------------------
type Aos struct {
	Port 						int
	Address					string
	StreamingType 	[]string

	AosServer 			string
	AosPort					int
	AosLogin				string
	AosPassword 		string


	api 				*aosrestapi.AosServerApi
	telegraf.Accumulator
	io.Closer
}

func (aos *Aos) Description() string {
	return "input Plugins for Apstra AOS Telemetry Streaming"
}

func (aos *Aos) SampleConfig() string {
	return `

  ## Maximum number of concurrent connections.

`
}

func (aos *Aos) Gather(_ telegraf.Accumulator) error {
	return nil
}


// Continuous Query that will refresh data every 15 sec
func (aos *Aos) RefreshData() {

    for {
      time.Sleep(30 * time.Second)
      aos.api.GetBlueprints()
      aos.api.GetSystems()
      log.Printf("D! Finished to Refresh Data, will sleep for %v sec", 30)
    }
}


func (aos *Aos) Start(acc telegraf.Accumulator) error {
	aos.Accumulator = acc

	// --------------------------------------------
	// Open Session to Rest API
	// --------------------------------------------
	aos.api = aosrestapi.NewAosServerApi(aos.AosServer, aos.AosPort, aos.AosLogin, aos.AosPassword)

	err := aos.api.Login()
	if err != nil { log.Printf("W! Error ", err)  }
	log.Printf("I! Session to AOS server Opened on %v:%v", aos.AosServer, aos.AosPort )

	// --------------------------------------------
	// Collect Blueprint and System info
	// --------------------------------------------
	err = aos.api.GetBlueprints()
	if err != nil {  log.Printf("W! Error ", err)  }

	err = aos.api.GetSystems()
	if err != nil {  log.Printf("W! Error ", err)  }

	for _, system := range aos.api.Systems {

		if system.Status.BlueprintId != "" {
			log.Printf("I! Id: %v - %v %s | %v", system.DeviceKey, system.UserConfig.AdminState, system.Status.BlueprintId, system.Blueprint.Role)
		} else {
			log.Printf("I! Id: %v - %v", system.DeviceKey, system.UserConfig.AdminState )
		}
	}

	// Launch Data Refresh in the Background
	go aos.RefreshData()

	// --------------------------------------------
	// Start Listening on TCP port
	// --------------------------------------------

	listenOn := fmt.Sprintf("0.0.0.0:%v", aos.Port)
	l, err := net.Listen("tcp", listenOn)
	if err != nil {
		return err
	}

	log.Printf("I! Listening on port %v", aos.Port)

	ssl := &streamAos{
		Listener: l,
		Aos: aos,
	}

	// --------------------------------------------
	// Configure Streaming on Server
	// --------------------------------------------
	for _, st := range aos.StreamingType {
		err = aos.api.StartStreaming(st, aos.Address, aos.Port)

		if err != nil {
			log.Printf("W! Unable to configure Streaming %v to %v:%v - %v", st, aos.Address, aos.Port, err)
		} else {
			log.Printf("I! Streaming of %v configured to %v:%v", st, aos.Address, aos.Port)
		}
	}

	go ssl.listen()

	return nil
}

func (aos *Aos) Stop() {
	if aos.Closer != nil {
		aos.Close()
		aos.Closer = nil
	}

	err := aos.api.StopStreaming()
	if err != nil {
		log.Printf("W! Error while stopping Streaming - %v", err)
	} else {
		log.Printf("I! Streaming stopped Successfully")
	}
}

func newAos() *Aos {
	return &Aos{}
}

func init() {
	inputs.Add("aos", func() telegraf.Input { return newAos() })
}
