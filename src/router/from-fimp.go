package router

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/thingsplex/sonos/sonos-api"

	"github.com/thingsplex/sonos/handler"

	"github.com/futurehomeno/fimpgo"
	log "github.com/sirupsen/logrus"
	"github.com/thingsplex/sonos/model"
)

type FromFimpRouter struct {
	inboundMsgCh fimpgo.MessageCh
	mqt          *fimpgo.MqttTransport
	instanceId   string
	appLifecycle *model.Lifecycle
	configs      *model.Configs
	states       *model.States
	pb           *handler.Playback
	vol          *handler.Volume
	client       *sonos.Client
}

func NewFromFimpRouter(mqt *fimpgo.MqttTransport, appLifecycle *model.Lifecycle, configs *model.Configs) *FromFimpRouter {
	fc := FromFimpRouter{inboundMsgCh: make(fimpgo.MessageCh, 5), mqt: mqt, appLifecycle: appLifecycle, configs: configs}
	fc.mqt.RegisterChannel("ch1", fc.inboundMsgCh)
	return &fc
}

func (fc *FromFimpRouter) Start() {

	fc.mqt.Subscribe(fmt.Sprintf("pt:j1/+/rt:dev/rn:%s/ad:1/#", model.ServiceName))
	fc.mqt.Subscribe(fmt.Sprintf("pt:j1/+/rt:ad/rn:%s/ad:1", model.ServiceName))

	go func(msgChan fimpgo.MessageCh) {
		for {
			select {
			case newMsg := <-msgChan:
				fc.routeFimpMessage(newMsg)
			}
		}
	}(fc.inboundMsgCh)
}

func (fc *FromFimpRouter) routeFimpMessage(newMsg *fimpgo.Message) {
	log.Debug("New fimp msg")
	addr := strings.Replace(newMsg.Addr.ServiceAddress, "_0", "", 1)
	switch newMsg.Payload.Service {

	case "media_player":

		switch newMsg.Payload.Type {
		case "cmd.playback.set":
			// get "play", "pause", "togglePlayPause", "skipToNextTrack" or "skipToPreviousTrack"
			val, err := newMsg.Payload.GetStringValue()
			if err != nil {
				log.Error("Ctrl error")
			}

			fc.pb.PlaybackSet(val, addr)

		case "cmd.playback_mode.set":
			// get str_map including bool values of repeat, repeatOne, crossfade and shuffle
			val, err := newMsg.Payload.GetStrMapValue()
			if err != nil {
				log.Error("Set mode error")
			}
			fc.pb.ModeSet(val, addr)
		case "cmd.volume.set":
			// get int from 0-100 representing new volume in %
			val, err := newMsg.Payload.GetIntValue()
			if err != nil {
				log.Error("Volume error")
			}
			fc.vol.VolumeSet(val, addr)
		}

	case model.ServiceName:
		adr := &fimpgo.Address{MsgType: fimpgo.MsgTypeEvt, ResourceType: fimpgo.ResourceTypeAdapter, ResourceName: model.ServiceName, ResourceAddress: "1"}
		switch newMsg.Payload.Type {

		case "cmd.auth.set_tokens":
			authReq := model.SetTokens{}
			err := newMsg.Payload.GetObjectValue(&authReq)
			if err != nil {
				log.Error("Incorrect login message ")
				return
			}
			status := model.AuthStatus{
				Status:    model.AuthStateAuthenticated,
				ErrorText: "",
				ErrorCode: "",
			}
			if authReq.AccessToken != "" && authReq.RefreshToken != "" {
				fc.configs.AccessToken = authReq.AccessToken
				fc.configs.RefreshToken = authReq.RefreshToken
				fc.configs.ExpiresIn = authReq.ExpiresIn
				fc.appLifecycle.SetAuthState(model.AuthStateAuthenticated)
				fc.appLifecycle.SetConnectionState(model.ConnStateConnected)
			} else {
				status.Status = "ERROR"
				status.ErrorText = "Empty username or password"
			}
			msg := fimpgo.NewMessage("evt.auth.status_report", model.ServiceName, fimpgo.VTypeObject, status, nil, nil, newMsg.Payload)
			if err := fc.mqt.RespondToRequest(newMsg.Payload, msg); err != nil {
				// if response topic is not set , sending back to default application event topic
				fc.mqt.Publish(adr, msg)
			}
			fc.configs.SaveToFile()

			// fc.client.GetHousehold(fc.configs.AccessToken)
			// fc.client.GetPlayersAndGroups(fc.configs.AccessToken, HOUSEHOLDID)

		case "cmd.auth.logout":
			// exclude all players
			// respond to wanted topic with necessary value(s)
			fc.configs.AccessToken = ""
			fc.appLifecycle.SetConfigState(model.ConfigStateNotConfigured)
			fc.appLifecycle.SetAuthState(model.AuthStateNotAuthenticated)
			fc.appLifecycle.SetConnectionState(model.ConnStateDisconnected)

			val2 := map[string]interface{}{
				"errors":  nil,
				"success": true,
			}
			msg := fimpgo.NewMessage("evt.pd7.response", "vinculum", fimpgo.VTypeObject, val2, nil, nil, newMsg.Payload)
			if err := fc.mqt.RespondToRequest(newMsg.Payload, msg); err != nil {
				log.Error("Could not respond to wanted request")
			}
			fc.configs.LoadDefaults()

		case "cmd.app.get_manifest":
			mode, err := newMsg.Payload.GetStringValue()
			if err != nil {
				log.Error("Incorrect request format ")
				return
			}
			manifest := model.NewManifest()
			err = manifest.LoadFromFile(filepath.Join(fc.configs.GetDefaultDir(), "app-manifest.json"))
			if err != nil {
				log.Error("Failed to load manifest file .Error :", err.Error())
				return
			}
			if mode == "manifest_state" {
				manifest.AppState = *fc.appLifecycle.GetAllStates()
				manifest.ConfigState = fc.configs
			}
			msg := fimpgo.NewMessage("evt.app.manifest_report", model.ServiceName, fimpgo.VTypeObject, manifest, nil, nil, newMsg.Payload)
			if err := fc.mqt.RespondToRequest(newMsg.Payload, msg); err != nil {
				// if response topic is not set , sending back to default application event topic
				fc.mqt.Publish(adr, msg)
			}

		case "cmd.app.get_state":
			msg := fimpgo.NewMessage("evt.app.manifest_report", model.ServiceName, fimpgo.VTypeObject, fc.appLifecycle.GetAllStates(), nil, nil, newMsg.Payload)
			if err := fc.mqt.RespondToRequest(newMsg.Payload, msg); err != nil {
				// if response topic is not set , sending back to default application event topic
				fc.mqt.Publish(adr, msg)
			}

		case "cmd.config.get_extended_report":

			msg := fimpgo.NewMessage("evt.config.extended_report", model.ServiceName, fimpgo.VTypeObject, fc.configs, nil, nil, newMsg.Payload)
			if err := fc.mqt.RespondToRequest(newMsg.Payload, msg); err != nil {
				fc.mqt.Publish(adr, msg)
			}

		case "cmd.config.extended_set":
			conf := model.Configs{}
			err := newMsg.Payload.GetObjectValue(&conf)
			if err != nil {
				// TODO: This is an example . Add your logic here or remove
				log.Error("Can't parse configuration object")
				return
			}
			fc.configs.Param1 = conf.Param1
			fc.configs.Param2 = conf.Param2
			fc.configs.SaveToFile()
			log.Debugf("App reconfigured . New parameters : %v", fc.configs)
			// TODO: This is an example . Add your logic here or remove
			configReport := model.ConfigReport{
				OpStatus: "ok",
				AppState: *fc.appLifecycle.GetAllStates(),
			}
			msg := fimpgo.NewMessage("evt.app.config_report", model.ServiceName, fimpgo.VTypeObject, configReport, nil, nil, newMsg.Payload)
			if err := fc.mqt.RespondToRequest(newMsg.Payload, msg); err != nil {
				fc.mqt.Publish(adr, msg)
			}

		case "cmd.log.set_level":
			// Configure log level
			level, err := newMsg.Payload.GetStringValue()
			if err != nil {
				return
			}
			logLevel, err := log.ParseLevel(level)
			if err == nil {
				log.SetLevel(logLevel)
				fc.configs.LogLevel = level
				fc.configs.SaveToFile()
			}
			log.Info("Log level updated to = ", logLevel)

		case "cmd.system.reconnect":
			// This is optional operation.
			fc.appLifecycle.PublishEvent(model.EventConfigured, "from-fimp-router", nil)
			//val := map[string]string{"status":status,"error":errStr}
			val := model.ButtonActionResponse{
				Operation:       "cmd.system.reconnect",
				OperationStatus: "ok",
				Next:            "config",
				ErrorCode:       "",
				ErrorText:       "",
			}
			msg := fimpgo.NewMessage("evt.app.config_action_report", model.ServiceName, fimpgo.VTypeObject, val, nil, nil, newMsg.Payload)
			if err := fc.mqt.RespondToRequest(newMsg.Payload, msg); err != nil {
				fc.mqt.Publish(adr, msg)
			}

		case "cmd.app.factory_reset":
			val := model.ButtonActionResponse{
				Operation:       "cmd.app.factory_reset",
				OperationStatus: "ok",
				Next:            "config",
				ErrorCode:       "",
				ErrorText:       "",
			}
			fc.appLifecycle.SetConfigState(model.ConfigStateNotConfigured)
			fc.appLifecycle.SetAppState(model.AppStateNotConfigured, nil)
			fc.appLifecycle.SetAuthState(model.AuthStateNotAuthenticated)
			msg := fimpgo.NewMessage("evt.app.config_action_report", model.ServiceName, fimpgo.VTypeObject, val, nil, nil, newMsg.Payload)
			if err := fc.mqt.RespondToRequest(newMsg.Payload, msg); err != nil {
				fc.mqt.Publish(adr, msg)
			}

		case "cmd.network.get_all_nodes":
			// TODO: This is an example . Add your logic here or remove
		case "cmd.thing.get_inclusion_report":
			//nodeId , _ := newMsg.Payload.GetStringValue()
			// TODO: This is an example . Add your logic here or remove
		case "cmd.thing.inclusion":
			//flag , _ := newMsg.Payload.GetBoolValue()
			// TODO: This is an example . Add your logic here or remove
		case "cmd.thing.delete":
			// remove device from network
			val, err := newMsg.Payload.GetStrMapValue()
			if err != nil {
				log.Error("Wrong msg format")
				return
			}
			deviceId, ok := val["address"]
			if ok {
				// TODO: This is an example . Add your logic here or remove
				log.Info(deviceId)
			} else {
				log.Error("Incorrect address")

			}
		}

	}

}