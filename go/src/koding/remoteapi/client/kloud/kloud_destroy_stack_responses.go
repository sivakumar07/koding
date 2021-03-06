package kloud

// This file was generated by the swagger tool.
// Editing this file might prove futile when you re-run the swagger generate command

import (
	"fmt"
	"io"

	"github.com/go-openapi/runtime"

	strfmt "github.com/go-openapi/strfmt"

	"koding/remoteapi/models"
)

// KloudDestroyStackReader is a Reader for the KloudDestroyStack structure.
type KloudDestroyStackReader struct {
	formats strfmt.Registry
}

// ReadResponse reads a server response into the received o.
func (o *KloudDestroyStackReader) ReadResponse(response runtime.ClientResponse, consumer runtime.Consumer) (interface{}, error) {
	switch response.Code() {

	case 200:
		result := NewKloudDestroyStackOK()
		if err := result.readResponse(response, consumer, o.formats); err != nil {
			return nil, err
		}
		return result, nil

	case 401:
		result := NewKloudDestroyStackUnauthorized()
		if err := result.readResponse(response, consumer, o.formats); err != nil {
			return nil, err
		}
		return nil, result

	default:
		return nil, runtime.NewAPIError("unknown error", response, response.Code())
	}
}

// NewKloudDestroyStackOK creates a KloudDestroyStackOK with default headers values
func NewKloudDestroyStackOK() *KloudDestroyStackOK {
	return &KloudDestroyStackOK{}
}

/*KloudDestroyStackOK handles this case with default header values.

Request processed successfully
*/
type KloudDestroyStackOK struct {
	Payload *models.DefaultResponse
}

func (o *KloudDestroyStackOK) Error() string {
	return fmt.Sprintf("[POST /remote.api/Kloud.destroyStack][%d] kloudDestroyStackOK  %+v", 200, o.Payload)
}

func (o *KloudDestroyStackOK) readResponse(response runtime.ClientResponse, consumer runtime.Consumer, formats strfmt.Registry) error {

	o.Payload = new(models.DefaultResponse)

	// response payload
	if err := consumer.Consume(response.Body(), o.Payload); err != nil && err != io.EOF {
		return err
	}

	return nil
}

// NewKloudDestroyStackUnauthorized creates a KloudDestroyStackUnauthorized with default headers values
func NewKloudDestroyStackUnauthorized() *KloudDestroyStackUnauthorized {
	return &KloudDestroyStackUnauthorized{}
}

/*KloudDestroyStackUnauthorized handles this case with default header values.

Unauthorized request
*/
type KloudDestroyStackUnauthorized struct {
	Payload *models.UnauthorizedRequest
}

func (o *KloudDestroyStackUnauthorized) Error() string {
	return fmt.Sprintf("[POST /remote.api/Kloud.destroyStack][%d] kloudDestroyStackUnauthorized  %+v", 401, o.Payload)
}

func (o *KloudDestroyStackUnauthorized) readResponse(response runtime.ClientResponse, consumer runtime.Consumer, formats strfmt.Registry) error {

	o.Payload = new(models.UnauthorizedRequest)

	// response payload
	if err := consumer.Consume(response.Body(), o.Payload); err != nil && err != io.EOF {
		return err
	}

	return nil
}
