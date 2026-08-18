// Package teamscontract defines the transport-independent Microsoft Teams
// identity and capability cohort shared by the Graph and Teams Web adapters.
package teamscontract

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/nkiyohara/corresync/internal/application"
)

const (
	chatPrefix    = "tgh1_"
	channelPrefix = "tgc1_"
	teamPrefix    = "tgt1_"
	componentJoin = "."
)

// Locator identifies either one chat or one channel without depending on the
// selected Teams transport.
type Locator struct {
	ChatID    string
	TeamID    string
	ChannelID string
}

func (locator Locator) IsChat() bool { return locator.ChatID != "" }

// Observation is bounded account-local evidence from the visible Teams app.
// Revision identifies a recognized semantic DOM contract, not a version
// inferred from branding.
type Observation struct {
	WorkspaceID  string
	Actor        application.MessageActor
	Capabilities application.MessageCapabilities
	Revision     string
}

// FullCohort is the maximum v0.9 Teams cohort. Both transport implementations
// must support an operation before it can remain true here. Account-observed
// permissions and UI controls can only narrow this manifest.
func FullCohort(readOnly bool) application.MessageCapabilities {
	write := !readOnly
	return application.MessageCapabilities{
		ListConversations: true,
		History:           true,
		SensitiveRead:     true,
		Search:            true,
		Send:              write,
		Reply:             write,
		Edit:              write,
		Delete:            write,
		Reactions:         write,
		// Teams Web keeps a newly addressed chat as a draft until its first
		// message is sent. The closed create operation has no initial-message
		// side effect, so Graph must not expose its broader standalone create.
		CreateConversation: false,
		Membership:         write,
		ActorMode:          application.MessageActorDelegatedUser,
	}
}

// ValidateWriteContent keeps Graph and Teams Web on one lossless composition
// contract. Both routes synthesize provider rich markup for typed mentions,
// but v0.9 does not accept caller-supplied HTML or Markdown.
func ValidateWriteContent(content application.MessageContent) error {
	if content.Format != application.MessageFormatPlain {
		return errors.New("the released Teams cohort accepts plain message content only")
	}
	return nil
}

// ValidateReaction restricts both routes to Microsoft's documented built-in
// Teams reaction vocabulary.
func ValidateReaction(name string) error {
	for _, candidate := range []string{"like", "heart", "laugh", "surprised", "sad", "angry"} {
		if name == candidate {
			return nil
		}
	}
	return fmt.Errorf("the Teams reaction %q is unsupported", name)
}

// Intersect narrows the released cohort to capabilities observed for one
// account. It deliberately cannot widen the common Graph/Web contract.
func Intersect(observed application.MessageCapabilities, readOnly bool) application.MessageCapabilities {
	cohort := FullCohort(readOnly)
	cohort.ListConversations = observed.ListConversations
	cohort.History = observed.History
	cohort.SensitiveRead = observed.SensitiveRead
	cohort.Search = observed.Search
	cohort.Send = cohort.Send && observed.Send
	cohort.Reply = cohort.Reply && observed.Reply
	cohort.Edit = cohort.Edit && observed.Edit
	cohort.Delete = cohort.Delete && observed.Delete
	cohort.Reactions = cohort.Reactions && observed.Reactions
	cohort.CreateConversation = cohort.CreateConversation && observed.CreateConversation
	cohort.Membership = cohort.Membership && observed.Membership
	return cohort
}

func EncodeChatID(id string) (string, error) {
	if !ValidOpaque(id) {
		return "", errors.New("the Teams chat ID is malformed")
	}
	return chatPrefix + base64.RawURLEncoding.EncodeToString([]byte(id)), nil
}

func EncodeTeamID(id string) (string, error) {
	if !ValidOpaque(id) {
		return "", errors.New("the Teams team ID is malformed")
	}
	return teamPrefix + base64.RawURLEncoding.EncodeToString([]byte(id)), nil
}

func EncodeChannelID(teamID, channelID string) (string, error) {
	if !ValidOpaque(teamID) || !ValidOpaque(channelID) {
		return "", errors.New("the Teams channel identity is malformed")
	}
	return channelPrefix + base64.RawURLEncoding.EncodeToString([]byte(teamID)) +
		componentJoin + base64.RawURLEncoding.EncodeToString([]byte(channelID)), nil
}

func DecodeConversationID(value string) (Locator, error) {
	if raw, found := strings.CutPrefix(value, chatPrefix); found {
		id, err := decodePart(raw)
		if err != nil {
			return Locator{}, errors.New("the Teams chat locator is malformed")
		}
		return Locator{ChatID: id}, nil
	}
	raw, found := strings.CutPrefix(value, channelPrefix)
	if !found {
		return Locator{}, errors.New("the Teams conversation locator is malformed")
	}
	team, channel, found := strings.Cut(raw, componentJoin)
	if !found || strings.Contains(channel, componentJoin) {
		return Locator{}, errors.New("the Teams channel locator is malformed")
	}
	teamID, teamErr := decodePart(team)
	channelID, channelErr := decodePart(channel)
	if teamErr != nil || channelErr != nil {
		return Locator{}, errors.New("the Teams channel locator is malformed")
	}
	return Locator{TeamID: teamID, ChannelID: channelID}, nil
}

func DecodeTeamID(value string) (string, error) {
	raw, found := strings.CutPrefix(value, teamPrefix)
	if !found {
		return "", errors.New("the Teams team container is malformed")
	}
	id, err := decodePart(raw)
	if err != nil {
		return "", errors.New("the Teams team container is malformed")
	}
	return id, nil
}

func ValidOpaque(value string) bool {
	return value != "" && len(value) <= 1024 && utf8.ValidString(value) &&
		!strings.ContainsAny(value, "\r\n\x00")
}

func decodePart(value string) (string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || !ValidOpaque(string(decoded)) ||
		base64.RawURLEncoding.EncodeToString(decoded) != value {
		return "", errors.New("the Teams locator component is malformed")
	}
	return string(decoded), nil
}
