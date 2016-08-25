package payment

import (
	"fmt"
	mongomodels "koding/db/models"
	"koding/db/mongodb/modelhelper"
	"socialapi/config"
	"socialapi/models"
	"socialapi/workers/common/tests"
	"socialapi/workers/email/emailsender"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
	"github.com/stripe/stripe-go"
	"github.com/stripe/stripe-go/currency"
	"github.com/stripe/stripe-go/plan"
	"gopkg.in/mgo.v2/bson"
)

func withStubData(f func(username string, groupName string, sessionID string)) {
	acc, _, groupName := models.CreateRandomGroupDataWithChecks()
	group, err := modelhelper.GetGroup(groupName)
	tests.ResultedWithNoErrorCheck(group, err)

	err = modelhelper.MakeAdmin(bson.ObjectIdHex(acc.OldId), group.Id)
	So(err, ShouldBeNil)

	ses, err := models.FetchOrCreateSession(acc.Nick, groupName)
	tests.ResultedWithNoErrorCheck(ses, err)

	cus, err := CreateCustomerForGroup(acc.Nick, groupName, &stripe.CustomerParams{})
	tests.ResultedWithNoErrorCheck(cus, err)

	f(acc.Nick, groupName, ses.ClientId)

	err = DeleteCustomerForGroup(groupName)
	So(err, ShouldBeNil)
}

func TestChargeSuccededHandler(t *testing.T) {
	testData := `
{
    "id": "ch_00000000000000",
    "object": "charge",
    "amount": 100,
    "currency": "usd",
    "customer": "%s",
    "description": "My First Test Charge (created for API docs)",
    "livemode": false,
    "paid": true,
    "status": "succeeded"
}`
	tests.WithConfiguration(t, func(c *config.Config) {
		stripe.Key = c.Stripe.SecretToken

		Convey("Given stub data", t, func() {
			withStubData(func(username, groupName, sessionID string) {
				Convey("Then Group should have customer id", func() {
					group, err := modelhelper.GetGroup(groupName)
					tests.ResultedWithNoErrorCheck(group, err)

					So(group.Payment.Customer.ID, ShouldNotBeBlank)

					Convey("When charge.succeeded is triggered", func() {

						raw := []byte(fmt.Sprintf(testData, group.Payment.Customer.ID))

						var capturedMail *emailsender.Mail

						realMailSender := mailSender
						mailSender = func(m *emailsender.Mail) error {
							capturedMail = m
							return nil
						}
						chargeSucceededHandler(raw)
						mailSender = realMailSender

						Convey("properties of event should be set accordingly", func() {
							So(capturedMail, ShouldNotBeNil)
							So(capturedMail.Subject, ShouldEqual, "charge succeeded")
							So(capturedMail.Properties.Options["amount"], ShouldEqual, "$1")
						})
					})
				})
			})
		})
	})
}

func TestChargeFailedHandler(t *testing.T) {
	testData := `
{
    "id": "ch_00000000000000",
    "object": "charge",
    "amount": 1000,
    "currency": "usd",
    "customer": "%s",
    "description": "My First Test Charge (created for API docs)",
    "livemode": false,
    "paid": false,
    "status": "succeeded"
}`
	tests.WithConfiguration(t, func(c *config.Config) {
		stripe.Key = c.Stripe.SecretToken

		Convey("Given stub data", t, func() {
			withStubData(func(username, groupName, sessionID string) {
				Convey("Then Group should have customer id", func() {
					group, err := modelhelper.GetGroup(groupName)
					tests.ResultedWithNoErrorCheck(group, err)

					So(group.Payment.Customer.ID, ShouldNotBeBlank)

					Convey("When charge.succeeded is triggered", func() {
						raw := []byte(fmt.Sprintf(testData, group.Payment.Customer.ID))

						var capturedMail *emailsender.Mail

						realMailSender := mailSender
						mailSender = func(m *emailsender.Mail) error {
							capturedMail = m
							return nil
						}
						chargeFailedHandler(raw)
						mailSender = realMailSender
						Convey("properties of event should be set accordingly", func() {
							So(capturedMail, ShouldNotBeNil)
							So(capturedMail.Subject, ShouldEqual, "charge failed")
							So(capturedMail.Properties.Options["amount"], ShouldEqual, "$10")
						})
					})
				})
			})
		})
	})
}

func TestInvoiceCreatedHandlerStayInTheSamePlan(t *testing.T) {
	testData := `
{
    "id": "in_00000000000000",
    "object": "invoice",
    "amount_due": 0,
    "application_fee": null,
    "attempt_count": 0,
    "attempted": false,
    "charge": null,
    "closed": false,
    "currency": "usd",
    "customer": "%s",
    "date": 1471348722,
    "description": null,
    "discount": null,
    "ending_balance": 0,
    "forgiven": false,
    "livemode": false,
    "metadata": {},
    "next_payment_attempt": null,
    "paid": false,
    "period_end": 1471348722,
    "period_start": 1471348722,
    "receipt_number": null,
    "starting_balance": 0,
    "statement_descriptor": null,
    "subscription": "%s",
    "subtotal": 0,
    "tax": null,
    "tax_percent": null,
    "total": %d,
    "webhooks_delivered_at": 1471348722
}`

	tests.WithConfiguration(t, func(c *config.Config) {
		stripe.Key = c.Stripe.SecretToken
		Convey("Given stub data", t, func() {
			withStubData(func(username, groupName, sessionID string) {
				group, err := modelhelper.GetGroup(groupName)
				tests.ResultedWithNoErrorCheck(group, err)

				withTestCreditCardToken(func(token string) {
					// attach payment source
					cp := &stripe.CustomerParams{
						Source: &stripe.SourceParams{
							Token: token,
						},
					}

					c, err := UpdateCustomerForGroup(username, groupName, cp)
					tests.ResultedWithNoErrorCheck(c, err)

					// generate 9 members with a total of 1 deleted user (also there is an admin)
					generateAndAddMembersToGroup(group.Id, 7)
					generateDeletedMemberAndAddToGroup(group.Id, 1)

					// create test plan
					id := fmt.Sprintf("p_%s", bson.NewObjectId().Hex())
					pp := &stripe.PlanParams{
						Amount:        Plans[UpTo10Users].Amount,
						Interval:      plan.Month,
						IntervalCount: 1,
						TrialPeriod:   0,
						Name:          id,
						Currency:      currency.USD,
						ID:            id,
					}

					p, err := plan.New(pp)
					So(err, ShouldBeNil)

					// subscribe to test plan
					params := &stripe.SubParams{
						Customer: group.Payment.Customer.ID,
						Plan:     p.ID,
						Quantity: 9,
					}

					sub, err := CreateSubscriptionForGroup(group.Slug, params)
					tests.ResultedWithNoErrorCheck(sub, err)

					// check if group has correct sub id
					groupAfterSub, err := modelhelper.GetGroup(groupName)
					tests.ResultedWithNoErrorCheck(groupAfterSub, err)
					So(sub.ID, ShouldEqual, groupAfterSub.Payment.Subscription.ID)

					Convey("When invoice.created is triggered with right amount of total fee", func() {
						raw := []byte(fmt.Sprintf(
							testData,
							group.Payment.Customer.ID,
							sub.ID,
							Plans[UpTo10Users].Amount*9,
						))

						err := invoiceCreatedHandler(raw)
						So(err, ShouldBeNil)

						Convey("subscription id should stay same", func() {
							groupAfterHook, err := modelhelper.GetGroup(groupName)
							tests.ResultedWithNoErrorCheck(groupAfterHook, err)

							// group should have correct sub id
							So(sub.ID, ShouldEqual, groupAfterHook.Payment.Subscription.ID)

							count, err := modelhelper.GetDeletedMemberCountByGroupId(group.Id)
							So(err, ShouldBeNil)
							So(count, ShouldEqual, 0)

							Convey("we should clean up successfully", func() {
								sub, err := DeleteSubscriptionForGroup(group.Slug)
								tests.ResultedWithNoErrorCheck(sub, err)

								_, err = plan.Del(pp.ID)
								So(err, ShouldBeNil)
							})
						})
					})
				})
			})
		})
	})
}

func TestInvoiceCreatedHandlerUpgradePlan(t *testing.T) {
	testData := `
{
    "id": "in_00000000000000",
    "object": "invoice",
    "amount_due": 0,
    "application_fee": null,
    "attempt_count": 0,
    "attempted": false,
    "charge": null,
    "closed": false,
    "currency": "usd",
    "customer": "%s",
    "date": 1471348722,
    "description": null,
    "discount": null,
    "ending_balance": 0,
    "forgiven": false,
    "livemode": false,
    "metadata": {},
    "next_payment_attempt": null,
    "paid": false,
    "period_end": 1471348722,
    "period_start": 1471348722,
    "receipt_number": null,
    "starting_balance": 0,
    "statement_descriptor": null,
    "subscription": "%s",
    "subtotal": 0,
    "tax": null,
    "tax_percent": null,
    "total": %d,
    "webhooks_delivered_at": 1471348722
}`

	tests.WithConfiguration(t, func(c *config.Config) {
		stripe.Key = c.Stripe.SecretToken
		Convey("Given stub data", t, func() {
			withStubData(func(username, groupName, sessionID string) {
				group, err := modelhelper.GetGroup(groupName)
				tests.ResultedWithNoErrorCheck(group, err)

				withTestCreditCardToken(func(token string) {
					// attach payment source
					cp := &stripe.CustomerParams{
						Source: &stripe.SourceParams{
							Token: token,
						},
					}

					c, err := UpdateCustomerForGroup(username, groupName, cp)
					tests.ResultedWithNoErrorCheck(c, err)

					// generate 9 members with a total of 1 deleted user (also there is an admin)
					generateAndAddMembersToGroup(group.Id, 7)
					generateDeletedMemberAndAddToGroup(group.Id, 1)

					// create test plan
					id := fmt.Sprintf("p_%s", bson.NewObjectId().Hex())
					pp := &stripe.PlanParams{
						Amount:        Plans[UpTo10Users].Amount,
						Interval:      plan.Month,
						IntervalCount: 1,
						TrialPeriod:   0,
						Name:          id,
						Currency:      currency.USD,
						ID:            id,
					}

					p, err := plan.New(pp)
					So(err, ShouldBeNil)

					// subscribe to test plan
					params := &stripe.SubParams{
						Customer: group.Payment.Customer.ID,
						Plan:     p.ID,
						Quantity: 9,
					}

					sub, err := CreateSubscriptionForGroup(group.Slug, params)
					tests.ResultedWithNoErrorCheck(sub, err)

					// check if group has correct sub id
					groupAfterSub, err := modelhelper.GetGroup(groupName)
					tests.ResultedWithNoErrorCheck(groupAfterSub, err)
					So(sub.ID, ShouldEqual, groupAfterSub.Payment.Subscription.ID)

					// add 1 more user to force plan upgrade
					generateAndAddMembersToGroup(group.Id, 1)

					Convey("When invoice.created is triggered with previous plan's amount", func() {
						raw := []byte(fmt.Sprintf(
							testData,
							group.Payment.Customer.ID,
							sub.ID,
							Plans[UpTo10Users].Amount*9,
						))

						So(invoiceCreatedHandler(raw), ShouldBeNil)

						Convey("subscription id should not stay same", func() {
							groupAfterHook, err := modelhelper.GetGroup(groupName)
							tests.ResultedWithNoErrorCheck(groupAfterHook, err)

							// group should have correct sub id
							So(sub.ID, ShouldNotEqual, groupAfterHook.Payment.Subscription.ID)

							sub, err := GetSubscriptionForGroup(groupName)
							tests.ResultedWithNoErrorCheck(sub, err)

							So(sub.Plan.ID, ShouldEqual, UpTo50Users)

							count, err := modelhelper.GetDeletedMemberCountByGroupId(group.Id)
							So(err, ShouldBeNil)
							So(count, ShouldEqual, 0)

							Convey("we should clean up successfully", func() {
								sub, err := DeleteSubscriptionForGroup(group.Slug)
								tests.ResultedWithNoErrorCheck(sub, err)

								_, err = plan.Del(pp.ID)
								So(err, ShouldBeNil)
							})
						})
					})
				})
			})
		})
	})
}

func TestInvoiceCreatedHandlerDowngradePlan(t *testing.T) {
	testData := `
{
    "id": "in_00000000000000",
    "object": "invoice",
    "amount_due": 0,
    "application_fee": null,
    "attempt_count": 0,
    "attempted": false,
    "charge": null,
    "closed": false,
    "currency": "usd",
    "customer": "%s",
    "date": 1471348722,
    "description": null,
    "discount": null,
    "ending_balance": 0,
    "forgiven": false,
    "livemode": false,
    "metadata": {},
    "next_payment_attempt": null,
    "paid": false,
    "period_end": 1471348722,
    "period_start": 1471348722,
    "receipt_number": null,
    "starting_balance": 0,
    "statement_descriptor": null,
    "subscription": "%s",
    "subtotal": 0,
    "tax": null,
    "tax_percent": null,
    "total": %d,
    "webhooks_delivered_at": 1471348722
}`

	tests.WithConfiguration(t, func(c *config.Config) {
		stripe.Key = c.Stripe.SecretToken
		Convey("Given stub data", t, func() {
			withStubData(func(username, groupName, sessionID string) {
				group, err := modelhelper.GetGroup(groupName)
				tests.ResultedWithNoErrorCheck(group, err)

				withTestCreditCardToken(func(token string) {
					// attach payment source
					cp := &stripe.CustomerParams{
						Source: &stripe.SourceParams{
							Token: token,
						},
					}

					c, err := UpdateCustomerForGroup(username, groupName, cp)
					tests.ResultedWithNoErrorCheck(c, err)

					// generate 9 members with a total of 1 deleted user (also there is an admin)
					generateAndAddMembersToGroup(group.Id, 7)
					generateDeletedMemberAndAddToGroup(group.Id, 1)

					// create test plan
					id := fmt.Sprintf("p_%s", bson.NewObjectId().Hex())
					pp := &stripe.PlanParams{
						Amount:        Plans[UpTo50Users].Amount,
						Interval:      plan.Month,
						IntervalCount: 1,
						TrialPeriod:   0,
						Name:          id,
						Currency:      currency.USD,
						ID:            id,
					}

					p, err := plan.New(pp)
					So(err, ShouldBeNil)

					// subscribe to test plan with more than actual number, simulating having 11 members previous month
					params := &stripe.SubParams{
						Customer: group.Payment.Customer.ID,
						Plan:     p.ID,
						Quantity: 11,
					}

					sub, err := CreateSubscriptionForGroup(group.Slug, params)
					tests.ResultedWithNoErrorCheck(sub, err)

					// check if group has correct sub id
					groupAfterSub, err := modelhelper.GetGroup(groupName)
					tests.ResultedWithNoErrorCheck(groupAfterSub, err)
					So(sub.ID, ShouldEqual, groupAfterSub.Payment.Subscription.ID)

					Convey("When invoice.created is triggered with previous plan's amount", func() {
						raw := []byte(fmt.Sprintf(
							testData,
							group.Payment.Customer.ID,
							sub.ID,
							Plans[UpTo10Users].Amount*11,
						))

						So(invoiceCreatedHandler(raw), ShouldBeNil)

						Convey("subscription id should not stay same", func() {
							groupAfterHook, err := modelhelper.GetGroup(groupName)
							tests.ResultedWithNoErrorCheck(groupAfterHook, err)

							// group should have correct sub id
							So(sub.ID, ShouldNotEqual, groupAfterHook.Payment.Subscription.ID)

							sub, err := GetSubscriptionForGroup(groupName)
							tests.ResultedWithNoErrorCheck(sub, err)

							So(sub.Plan.ID, ShouldEqual, UpTo10Users)

							count, err := modelhelper.GetDeletedMemberCountByGroupId(group.Id)
							So(err, ShouldBeNil)
							So(count, ShouldEqual, 0)

							Convey("we should clean up successfully", func() {
								sub, err := DeleteSubscriptionForGroup(group.Slug)
								tests.ResultedWithNoErrorCheck(sub, err)

								_, err = plan.Del(pp.ID)
								So(err, ShouldBeNil)
							})
						})
					})
				})
			})
		})
	})
}

func generateAndAddMembersToGroup(groupID bson.ObjectId, count int) {
	// generate members
	for i := 0; i < count; i++ {
		account := models.CreateAccountInBothDbsWithCheck()
		acc, err := modelhelper.GetAccount(account.Nick)
		tests.ResultedWithNoErrorCheck(acc, err)

		err = modelhelper.AddRelationship(&mongomodels.Relationship{
			Id:         bson.NewObjectId(),
			TargetId:   acc.Id,
			TargetName: "JAccount",
			SourceId:   groupID,
			SourceName: "JGroup",
			As:         "member",
		})
		So(err, ShouldBeNil)
	}
}

func generateDeletedMemberAndAddToGroup(groupID bson.ObjectId, count int) {
	// generate members
	for i := 0; i < count; i++ {
		account1 := models.CreateAccountInBothDbsWithCheck()
		acc, err := modelhelper.GetAccount(account1.Nick)
		tests.ResultedWithNoErrorCheck(acc, err)
		dm, err := modelhelper.CreateDeletedMember(groupID, acc.Id)
		tests.ResultedWithNoErrorCheck(dm, err)
	}
}

var webhookTestData = map[string]string{
	"invalid.event_name": `{
    "created": 1326853478,
    "livemode": false,
    "id": "evt_00000000000000",
    "type": "invalid.event_name",
    "object": "event",
    "request": null,
    "pending_webhooks": 1,
    "api_version": "2016-07-06"
}`,

	"charge.succeeded": `{
    "created": 1326853478,
    "livemode": false,
    "id": "evt_00000000000000",
    "type": "charge.succeeded",
    "object": "event",
    "request": null,
    "pending_webhooks": 1,
    "api_version": "2016-07-06",
    "data": {
      "object": {
      "id": "ch_00000000000000",
      "object": "charge",
      "amount": 100,
      "amount_refunded": 0,
      "application_fee": null,
      "balance_transaction": "txn_00000000000000",
      "captured": true,
      "created": 1471522245,
      "currency": "usd",
      "customer": null,
      "description": "My First Test Charge (created for API docs)",
      "destination": null,
      "dispute": null,
      "failure_code": null,
      "failure_message": null,
      "fraud_details": {},
      "invoice": null,
      "livemode": false,
      "metadata": {},
      "order": null,
      "paid": true,
      "receipt_email": null,
      "receipt_number": null,
      "refunded": false,
      "refunds": {
        "object": "list",
        "data": [],
        "has_more": false,
        "total_count": 0,
        "url": "/v1/charges/ch_18jpM9Aub2qoNeqqEMn1oI70/refunds"
      },
      "shipping": null,
      "source": {
        "id": "card_00000000000000",
        "object": "card",
        "address_city": null,
        "address_country": null,
        "address_line1": null,
        "address_line1_check": null,
        "address_line2": null,
        "address_state": null,
        "address_zip": null,
        "address_zip_check": null,
        "brand": "Visa",
        "country": "US",
        "customer": "cus_00000000000000",
        "cvc_check": "pass",
        "dynamic_last4": null,
        "exp_month": 12,
        "exp_year": 2020,
        "funding": "credit",
        "last4": "4242",
        "metadata": {},
        "name": null,
        "tokenization_method": null
      },
      "source_transfer": null,
      "statement_descriptor": null,
      "status": "succeeded"
      }
    }
}`,
	"charge.failed": `{
    "created": 1326853478,
    "livemode": false,
    "id": "evt_00000000000000",
    "type": "charge.failed",
    "object": "event",
    "request": null,
    "pending_webhooks": 1,
    "api_version": "2016-07-06",
    "data": {
        "object": {
            "id": "ch_00000000000000",
            "object": "charge",
            "amount": 100,
            "amount_refunded": 0,
            "application_fee": null,
            "balance_transaction": "txn_00000000000000",
            "captured": true,
            "created": 1471522438,
            "currency": "usd",
            "customer": null,
            "description": "My First Test Charge (created for API docs)",
            "destination": null,
            "dispute": null,
            "failure_code": null,
            "failure_message": null,
            "fraud_details": {},
            "invoice": null,
            "livemode": false,
            "metadata": {},
            "order": null,
            "paid": false,
            "receipt_email": null,
            "receipt_number": null,
            "refunded": false,
            "refunds": {
                "object": "list",
                "data": [],
                "has_more": false,
                "total_count": 0,
                "url": "/v1/charges/ch_18jpPGAub2qoNeqqei5jINte/refunds"
            },
            "shipping": null,
            "source": {
                "id": "card_00000000000000",
                "object": "card",
                "address_city": null,
                "address_country": null,
                "address_line1": null,
                "address_line1_check": null,
                "address_line2": null,
                "address_state": null,
                "address_zip": null,
                "address_zip_check": null,
                "brand": "Visa",
                "country": "US",
                "customer": "cus_00000000000000",
                "cvc_check": "pass",
                "dynamic_last4": null,
                "exp_month": 12,
                "exp_year": 2020,
                "funding": "credit",
                "last4": "4242",
                "metadata": {},
                "name": null,
                "tokenization_method": null
            },
            "source_transfer": null,
            "statement_descriptor": null,
            "status": "succeeded"
        }
    }
}`,
	"customer.subscription.created": `{
    "created": 1326853478,
    "livemode": false,
    "id": "evt_00000000000000",
    "type": "customer.subscription.created",
    "object": "event",
    "request": null,
    "pending_webhooks": 1,
    "api_version": "2016-07-06",
    "data": {
        "object": {
            "id": "sub_00000000000000",
            "object": "subscription",
            "application_fee_percent": null,
            "cancel_at_period_end": false,
            "canceled_at": 1471348722,
            "created": 1471348722,
            "current_period_end": 1474027122,
            "current_period_start": 1471348722,
            "customer": "cus_00000000000000",
            "discount": null,
            "ended_at": 1471348722,
            "livemode": false,
            "metadata": {},
            "plan": {
                "id": "p_00000000000000",
                "object": "plan",
                "amount": 0,
                "created": 1471348721,
                "currency": "usd",
                "interval": "month",
                "interval_count": 1,
                "livemode": false,
                "metadata": {},
                "name": "Free Forever",
                "statement_descriptor": "FREE",
                "trial_period_days": null
            },
            "quantity": 1,
            "start": 1471348722,
            "status": "canceled",
            "tax_percent": null,
            "trial_end": null,
            "trial_start": null
        }
    }
}`,
	"customer.subscription.deleted": `{
    "created": 1326853478,
    "livemode": false,
    "id": "evt_00000000000000",
    "type": "customer.subscription.deleted",
    "object": "event",
    "request": null,
    "pending_webhooks": 1,
    "api_version": "2016-07-06",
    "data": {
        "object": {
            "id": "sub_00000000000000",
            "object": "subscription",
            "application_fee_percent": null,
            "cancel_at_period_end": false,
            "canceled_at": 1471348722,
            "created": 1471348722,
            "current_period_end": 1474027122,
            "current_period_start": 1471348722,
            "customer": "cus_00000000000000",
            "discount": null,
            "ended_at": 1471476413,
            "livemode": false,
            "metadata": {},
            "plan": {
                "id": "p_00000000000000",
                "object": "plan",
                "amount": 0,
                "created": 1471348721,
                "currency": "usd",
                "interval": "month",
                "interval_count": 1,
                "livemode": false,
                "metadata": {},
                "name": "Free Forever",
                "statement_descriptor": "FREE",
                "trial_period_days": null
            },
            "quantity": 1,
            "start": 1471348722,
            "status": "canceled",
            "tax_percent": null,
            "trial_end": null,
            "trial_start": null
        }
    }
}`,
	"customer.subscription.updated": `{
    "created": 1326853478,
    "livemode": false,
    "id": "evt_00000000000000",
    "type": "customer.subscription.updated",
    "object": "event",
    "request": null,
    "pending_webhooks": 1,
    "api_version": "2016-07-06",
    "data": {
        "object": {
            "id": "sub_00000000000000",
            "object": "subscription",
            "application_fee_percent": null,
            "cancel_at_period_end": false,
            "canceled_at": 1471348722,
            "created": 1471348722,
            "current_period_end": 1474027122,
            "current_period_start": 1471348722,
            "customer": "cus_00000000000000",
            "discount": null,
            "ended_at": 1471348722,
            "livemode": false,
            "metadata": {},
            "plan": {
                "id": "p_00000000000000",
                "object": "plan",
                "amount": 0,
                "created": 1471348721,
                "currency": "usd",
                "interval": "month",
                "interval_count": 1,
                "livemode": false,
                "metadata": {},
                "name": "Free Forever",
                "statement_descriptor": "FREE",
                "trial_period_days": null
            },
            "quantity": 1,
            "start": 1471348722,
            "status": "canceled",
            "tax_percent": null,
            "trial_end": null,
            "trial_start": null
        },
        "previous_attributes": {
            "plan": {
                "id": "OLD_PLAN_ID",
                "object": "plan",
                "amount": 0,
                "created": 1471339133,
                "currency": "usd",
                "interval": "month",
                "interval_count": 1,
                "livemode": false,
                "metadata": {},
                "name": "Old plan",
                "statement_descriptor": "FREE",
                "trial_period_days": null
            }
        }
    }
}`,
	"customer.subscription.trial_will_end": `{
    "created": 1326853478,
    "livemode": false,
    "id": "evt_00000000000000",
    "type": "customer.subscription.trial_will_end",
    "object": "event",
    "request": null,
    "pending_webhooks": 1,
    "api_version": "2016-07-06",
    "data": {
        "object": {
            "id": "sub_00000000000000",
            "object": "subscription",
            "application_fee_percent": null,
            "cancel_at_period_end": false,
            "canceled_at": 1471348722,
            "created": 1471348722,
            "current_period_end": 1474027122,
            "current_period_start": 1471348722,
            "customer": "cus_00000000000000",
            "discount": null,
            "ended_at": 1471348722,
            "livemode": false,
            "metadata": {},
            "plan": {
                "id": "p_00000000000000",
                "object": "plan",
                "amount": 0,
                "created": 1471348721,
                "currency": "usd",
                "interval": "month",
                "interval_count": 1,
                "livemode": false,
                "metadata": {},
                "name": "Free Forever",
                "statement_descriptor": "FREE",
                "trial_period_days": null
            },
            "quantity": 1,
            "start": 1471348722,
            "status": "trialing",
            "tax_percent": null,
            "trial_end": 1471735613,
            "trial_start": 1471476413
        }
    }
}
`,
	"invoice.created": `{
    "created": 1326853478,
    "livemode": false,
    "id": "evt_00000000000000",
    "type": "invoice.created",
    "object": "event",
    "request": null,
    "pending_webhooks": 1,
    "api_version": "2016-07-06",
    "data": {
        "object": {
            "id": "in_00000000000000",
            "object": "invoice",
            "amount_due": 0,
            "application_fee": null,
            "attempt_count": 0,
            "attempted": false,
            "charge": null,
            "closed": true,
            "currency": "usd",
            "customer": "cus_00000000000000",
            "date": 1471348722,
            "description": null,
            "discount": null,
            "ending_balance": 0,
            "forgiven": false,
            "lines": {
                "data": [
                    {
                        "id": "sub_918UwtRVQpmBpX",
                        "object": "line_item",
                        "amount": 0,
                        "currency": "usd",
                        "description": null,
                        "discountable": true,
                        "livemode": true,
                        "metadata": {},
                        "period": {
                            "start": 1474027122,
                            "end": 1476619122
                        },
                        "plan": {
                            "id": "p_57b2da7d9bc22b6280dba16c",
                            "object": "plan",
                            "amount": 0,
                            "created": 1471339133,
                            "currency": "usd",
                            "interval": "month",
                            "interval_count": 1,
                            "livemode": false,
                            "metadata": {},
                            "name": "Free Forever",
                            "statement_descriptor": "FREE",
                            "trial_period_days": null
                        },
                        "proration": false,
                        "quantity": 1,
                        "subscription": null,
                        "type": "subscription"
                    }
                ],
                "total_count": 1,
                "object": "list",
                "url": "/v1/invoices/in_18j6DOAub2qoNeqqzCbMjcIC/lines"
            },
            "livemode": false,
            "metadata": {},
            "next_payment_attempt": null,
            "paid": true,
            "period_end": 1471348722,
            "period_start": 1471348722,
            "receipt_number": null,
            "starting_balance": 0,
            "statement_descriptor": null,
            "subscription": "sub_00000000000000",
            "subtotal": 0,
            "tax": null,
            "tax_percent": null,
            "total": 0,
            "webhooks_delivered_at": 1471348722
        }
    }
}`,
	"invoice.payment_failed": `{
    "created": 1326853478,
    "livemode": false,
    "id": "evt_00000000000000",
    "type": "invoice.payment_failed",
    "object": "event",
    "request": null,
    "pending_webhooks": 1,
    "api_version": "2016-07-06",
    "data": {
        "object": {
            "id": "in_00000000000000",
            "object": "invoice",
            "amount_due": 0,
            "application_fee": null,
            "attempt_count": 0,
            "attempted": true,
            "charge": null,
            "closed": false,
            "currency": "usd",
            "customer": "cus_00000000000000",
            "date": 1471348722,
            "description": null,
            "discount": null,
            "ending_balance": 0,
            "forgiven": false,
            "lines": {
                "data": [
                    {
                        "id": "sub_918UwtRVQpmBpX",
                        "object": "line_item",
                        "amount": 0,
                        "currency": "usd",
                        "description": null,
                        "discountable": true,
                        "livemode": true,
                        "metadata": {},
                        "period": {
                            "start": 1474027122,
                            "end": 1476619122
                        },
                        "plan": {
                            "id": "p_57b2da7d9bc22b6280dba16c",
                            "object": "plan",
                            "amount": 0,
                            "created": 1471339133,
                            "currency": "usd",
                            "interval": "month",
                            "interval_count": 1,
                            "livemode": false,
                            "metadata": {},
                            "name": "Free Forever",
                            "statement_descriptor": "FREE",
                            "trial_period_days": null
                        },
                        "proration": false,
                        "quantity": 1,
                        "subscription": null,
                        "type": "subscription"
                    }
                ],
                "total_count": 1,
                "object": "list",
                "url": "/v1/invoices/in_18j6DOAub2qoNeqqzCbMjcIC/lines"
            },
            "livemode": false,
            "metadata": {},
            "next_payment_attempt": null,
            "paid": false,
            "period_end": 1471348722,
            "period_start": 1471348722,
            "receipt_number": null,
            "starting_balance": 0,
            "statement_descriptor": null,
            "subscription": "sub_00000000000000",
            "subtotal": 0,
            "tax": null,
            "tax_percent": null,
            "total": 0,
            "webhooks_delivered_at": 1471348722
        }
    }
}`,
	"invoice.payment_succeeded": `{
    "created": 1326853478,
    "livemode": false,
    "id": "evt_00000000000000",
    "type": "invoice.payment_succeeded",
    "object": "event",
    "request": null,
    "pending_webhooks": 1,
    "api_version": "2016-07-06",
    "data": {
        "object": {
            "id": "in_00000000000000",
            "object": "invoice",
            "amount_due": 0,
            "application_fee": null,
            "attempt_count": 0,
            "attempted": true,
            "charge": "_00000000000000",
            "closed": true,
            "currency": "usd",
            "customer": "cus_00000000000000",
            "date": 1471348722,
            "description": null,
            "discount": null,
            "ending_balance": 0,
            "forgiven": false,
            "lines": {
                "data": [
                    {
                        "id": "sub_918UwtRVQpmBpX",
                        "object": "line_item",
                        "amount": 0,
                        "currency": "usd",
                        "description": null,
                        "discountable": true,
                        "livemode": true,
                        "metadata": {},
                        "period": {
                            "start": 1474027122,
                            "end": 1476619122
                        },
                        "plan": {
                            "id": "p_57b2da7d9bc22b6280dba16c",
                            "object": "plan",
                            "amount": 0,
                            "created": 1471339133,
                            "currency": "usd",
                            "interval": "month",
                            "interval_count": 1,
                            "livemode": false,
                            "metadata": {},
                            "name": "Free Forever",
                            "statement_descriptor": "FREE",
                            "trial_period_days": null
                        },
                        "proration": false,
                        "quantity": 1,
                        "subscription": null,
                        "type": "subscription"
                    }
                ],
                "total_count": 1,
                "object": "list",
                "url": "/v1/invoices/in_18j6DOAub2qoNeqqzCbMjcIC/lines"
            },
            "livemode": false,
            "metadata": {},
            "next_payment_attempt": null,
            "paid": true,
            "period_end": 1471348722,
            "period_start": 1471348722,
            "receipt_number": null,
            "starting_balance": 0,
            "statement_descriptor": null,
            "subscription": "sub_00000000000000",
            "subtotal": 0,
            "tax": null,
            "tax_percent": null,
            "total": 0,
            "webhooks_delivered_at": 1471348722
        }
    }
}`,
}
