package teamsgraph

import (
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
)

const (
	chatLocatorPrefix      = "tgh1_"
	channelLocatorPrefix   = "tgc1_"
	teamContainerPrefix    = "tgt1_"
	locatorComponentJoiner = "."
)

type graphConversationLocator struct {
	ChatID    string
	TeamID    string
	ChannelID string
}

func (locator graphConversationLocator) isChat() bool { return locator.ChatID != "" }

func encodeGraphChatID(id string) (string, error) {
	if !validGraphOpaque(id) {
		return "", errors.New("the Microsoft Graph chat ID is malformed")
	}
	return chatLocatorPrefix + base64.RawURLEncoding.EncodeToString([]byte(id)), nil
}

func encodeGraphTeamID(id string) (string, error) {
	if !validGraphOpaque(id) {
		return "", errors.New("the Microsoft Graph team ID is malformed")
	}
	return teamContainerPrefix + base64.RawURLEncoding.EncodeToString([]byte(id)), nil
}

func encodeGraphChannelID(teamID, channelID string) (string, error) {
	if !validGraphOpaque(teamID) || !validGraphOpaque(channelID) {
		return "", errors.New("the Microsoft Graph channel identity is malformed")
	}
	return channelLocatorPrefix + base64.RawURLEncoding.EncodeToString([]byte(teamID)) +
		locatorComponentJoiner + base64.RawURLEncoding.EncodeToString([]byte(channelID)), nil
}

func decodeGraphConversationID(value string) (graphConversationLocator, error) {
	if raw, found := strings.CutPrefix(value, chatLocatorPrefix); found {
		id, err := decodeGraphLocatorPart(raw)
		if err != nil {
			return graphConversationLocator{}, errors.New("the Teams Graph chat locator is malformed")
		}
		return graphConversationLocator{ChatID: id}, nil
	}
	raw, found := strings.CutPrefix(value, channelLocatorPrefix)
	if !found {
		return graphConversationLocator{}, errors.New("the Teams Graph conversation locator is malformed")
	}
	team, channel, found := strings.Cut(raw, locatorComponentJoiner)
	if !found || strings.Contains(channel, locatorComponentJoiner) {
		return graphConversationLocator{}, errors.New("the Teams Graph channel locator is malformed")
	}
	teamID, teamErr := decodeGraphLocatorPart(team)
	channelID, channelErr := decodeGraphLocatorPart(channel)
	if teamErr != nil || channelErr != nil {
		return graphConversationLocator{}, errors.New("the Teams Graph channel locator is malformed")
	}
	return graphConversationLocator{TeamID: teamID, ChannelID: channelID}, nil
}

func decodeGraphTeamID(value string) (string, error) {
	raw, found := strings.CutPrefix(value, teamContainerPrefix)
	if !found {
		return "", errors.New("the Teams Graph team container is malformed")
	}
	id, err := decodeGraphLocatorPart(raw)
	if err != nil {
		return "", errors.New("the Teams Graph team container is malformed")
	}
	return id, nil
}

func decodeGraphLocatorPart(value string) (string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || !validGraphOpaque(string(decoded)) ||
		base64.RawURLEncoding.EncodeToString(decoded) != value {
		return "", errors.New("the Teams Graph locator component is malformed")
	}
	return string(decoded), nil
}

func (locator graphConversationLocator) collectionResource(threadRootID string) (string, error) {
	if locator.isChat() {
		if threadRootID != "" {
			return "", errors.New("the Teams chat does not expose threaded history")
		}
		return "chats/" + graphPathSegment(locator.ChatID) + "/messages", nil
	}
	base := "teams/" + graphPathSegment(locator.TeamID) + "/channels/" +
		graphPathSegment(locator.ChannelID) + "/messages"
	if threadRootID != "" {
		if !validGraphOpaque(threadRootID) {
			return "", errors.New("the Teams channel thread root ID is malformed")
		}
		base += "/" + graphPathSegment(threadRootID) + "/replies"
	}
	return base, nil
}

func (locator graphConversationLocator) messageResource(threadRootID, messageID string) (string, error) {
	if !validGraphOpaque(messageID) {
		return "", errors.New("the Teams Graph message ID is malformed")
	}
	collection, err := locator.collectionResource(threadRootID)
	if err != nil {
		return "", err
	}
	return collection + "/" + graphPathSegment(messageID), nil
}

func graphPathSegment(value string) string { return url.PathEscape(value) }
