package imapmail

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"strings"

	"golang.org/x/net/html"

	"github.com/nkiyohara/corresync/internal/application"
)

type parsedMIME struct {
	Text        string
	Attachments []mimeAttachment
	Header      mail.Header
}

type mimeAttachment struct {
	Part        int
	Name        string
	ContentType string
	Content     []byte
	Inline      bool
	ContentID   string
}

func parseMIME(raw []byte) (parsedMIME, error) {
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return parsedMIME{}, fmt.Errorf("parse MIME message: %w", err)
	}
	result := parsedMIME{
		Header: message.Header, Attachments: make([]mimeAttachment, 0, 4),
	}
	part := 0
	var plain, htmlBody strings.Builder
	if err := walkMIME(
		textproto.MIMEHeader(message.Header),
		message.Body,
		&part,
		&plain,
		&htmlBody,
		&result.Attachments,
	); err != nil {
		return parsedMIME{}, err
	}
	if plain.Len() != 0 {
		result.Text = plain.String()
	} else {
		result.Text, err = htmlToText(htmlBody.String())
		if err != nil {
			return parsedMIME{}, err
		}
	}
	if len(result.Text) > application.MaxMailBodyBytes {
		return parsedMIME{}, fmt.Errorf(
			"mail body exceeds %d bytes",
			application.MaxMailBodyBytes,
		)
	}
	return result, nil
}

func walkMIME(
	header textproto.MIMEHeader,
	body io.Reader,
	part *int,
	plain, htmlBody *strings.Builder,
	attachments *[]mimeAttachment,
) error {
	mediaType, parameters, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil || mediaType == "" {
		mediaType = "text/plain"
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := parameters["boundary"]
		if boundary == "" {
			return errors.New("multipart message has no boundary")
		}
		reader := multipart.NewReader(body, boundary)
		for {
			child, err := reader.NextPart()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("read MIME part: %w", err)
			}
			if err := walkMIME(child.Header, child, part, plain, htmlBody, attachments); err != nil {
				_ = child.Close()
				return err
			}
			_ = child.Close()
		}
	}
	*part++
	content, err := readDecodedPart(header, body)
	if err != nil {
		return err
	}
	disposition, dispositionParams, _ := mime.ParseMediaType(header.Get("Content-Disposition"))
	name := dispositionParams["filename"]
	if name == "" {
		name = parameters["name"]
	}
	inline := strings.EqualFold(disposition, "inline")
	if name != "" || strings.EqualFold(disposition, "attachment") || inline {
		if len(*attachments) >= application.MaxMailAttachmentMetadata {
			return errors.New("mail attachment metadata count exceeds the limit")
		}
		if len(content) > application.MaxMailAttachmentBytes {
			return fmt.Errorf(
				"mail attachment exceeds %d bytes",
				application.MaxMailAttachmentBytes,
			)
		}
		if name == "" {
			name = fmt.Sprintf("part-%d", *part)
		}
		*attachments = append(*attachments, mimeAttachment{
			Part: *part, Name: name, ContentType: mediaType, Content: content,
			Inline: inline, ContentID: strings.Trim(header.Get("Content-ID"), "<>"),
		})
		return nil
	}
	switch strings.ToLower(mediaType) {
	case "text/plain":
		return appendMIMEText(plain, string(content))
	case "text/html":
		return appendMIMEText(htmlBody, string(content))
	default:
		return nil
	}
}

func readDecodedPart(header textproto.MIMEHeader, body io.Reader) ([]byte, error) {
	reader := body
	switch strings.ToLower(header.Get("Content-Transfer-Encoding")) {
	case "", "7bit", "8bit", "binary":
	case "base64":
		reader = base64.NewDecoder(base64.StdEncoding, body)
	case "quoted-printable":
		reader = quotedprintable.NewReader(body)
	default:
		return nil, errors.New("unsupported content transfer encoding")
	}
	content, err := io.ReadAll(io.LimitReader(reader, maximumRawMessageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("decode MIME part: %w", err)
	}
	if len(content) > maximumRawMessageBytes {
		return nil, errors.New("decoded MIME part exceeds the limit")
	}
	return content, nil
}

func appendMIMEText(builder *strings.Builder, value string) error {
	separator := 0
	if builder.Len() != 0 {
		separator = 1
	}
	if len(value) > application.MaxMailBodyBytes-builder.Len()-separator {
		return fmt.Errorf("mail body exceeds %d bytes", application.MaxMailBodyBytes)
	}
	if separator != 0 {
		builder.WriteByte('\n')
	}
	builder.WriteString(value)
	return nil
}

func htmlToText(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	root, err := html.Parse(strings.NewReader(value))
	if err != nil {
		return "", fmt.Errorf("parse HTML mail body: %w", err)
	}
	var builder strings.Builder
	var walkErr error
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if walkErr != nil {
			return
		}
		if node.Type == html.TextNode {
			text := strings.TrimSpace(node.Data)
			if text != "" {
				walkErr = appendMIMEText(&builder, text)
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return builder.String(), walkErr
}

type attachmentReference struct {
	Message   string `json:"message"`
	ChangeKey string `json:"changeKey"`
	Part      int    `json:"part"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Size      int    `json:"size"`
	Inline    bool   `json:"inline"`
	CID       string `json:"cid,omitempty"`
}

func encodeAttachmentID(reference attachmentReference) (string, error) {
	data, err := json.Marshal(reference)
	if err != nil {
		return "", err
	}
	return "iat1_" + base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeAttachmentID(value string) (attachmentReference, error) {
	if !strings.HasPrefix(value, "iat1_") {
		return attachmentReference{}, errors.New("attachment ID is not an IMAP identifier")
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "iat1_"))
	if err != nil || len(data) > 4096 {
		return attachmentReference{}, errors.New("IMAP attachment ID is malformed")
	}
	var reference attachmentReference
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reference); err != nil ||
		reference.Message == "" || reference.ChangeKey == "" ||
		reference.Part < 1 || reference.Name == "" ||
		reference.Type == "" || reference.Size < 0 ||
		reference.Size > application.MaxMailAttachmentBytes {
		return attachmentReference{}, errors.New("IMAP attachment ID is malformed")
	}
	return reference, nil
}

func encodeBase64(value []byte) string {
	return base64.StdEncoding.EncodeToString(value)
}
