package telegram

import (
	"testing"

	"github.com/mymmrac/telego"
)

func TestImageFileIDsTakesLargestPhotoAndReply(t *testing.T) {
	msg := &telego.Message{
		Photo: []telego.PhotoSize{{FileID: "small"}, {FileID: "large"}},
		ReplyToMessage: &telego.Message{
			Document: &telego.Document{FileID: "doc", MimeType: "image/png"},
		},
	}
	ids := imageFileIDs(msg)
	if len(ids) != 2 || ids[0] != "large" || ids[1] != "doc" {
		t.Fatalf("%v", ids)
	}
	if imageFileIDs(&telego.Message{Document: &telego.Document{FileID: "x", MimeType: "application/pdf"}}) != nil {
		t.Fatal("pdf не картинка")
	}
	if mimeByPath("photos/a.PNG") != "image/png" {
		t.Fatal("mime")
	}
}
