package teamsgraph

import (
	"errors"
	"net/url"

	"github.com/nkiyohara/corresync/internal/provider/teamscontract"
)

type graphConversationLocator struct {
	ChatID    string
	TeamID    string
	ChannelID string
}

func (locator graphConversationLocator) isChat() bool { return locator.ChatID != "" }

func encodeGraphChatID(id string) (string, error) {
	return teamscontract.EncodeChatID(id)
}

func encodeGraphTeamID(id string) (string, error) {
	return teamscontract.EncodeTeamID(id)
}

func encodeGraphChannelID(teamID, channelID string) (string, error) {
	return teamscontract.EncodeChannelID(teamID, channelID)
}

func decodeGraphConversationID(value string) (graphConversationLocator, error) {
	locator, err := teamscontract.DecodeConversationID(value)
	if err != nil {
		return graphConversationLocator{}, err
	}
	return graphConversationLocator{
		ChatID: locator.ChatID, TeamID: locator.TeamID, ChannelID: locator.ChannelID,
	}, nil
}

func decodeGraphTeamID(value string) (string, error) {
	return teamscontract.DecodeTeamID(value)
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
