package ec2

import (
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/arn"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/hashicorp/aws-sdk-go-base/v2/awsv1shim/v2/tfawserr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-provider-aws/internal/conns"
	"github.com/hashicorp/terraform-provider-aws/internal/flex"
	tftags "github.com/hashicorp/terraform-provider-aws/internal/tags"
	"github.com/hashicorp/terraform-provider-aws/internal/verify"
)

func ResourceVPCEndpointService() *schema.Resource {
	return &schema.Resource{
		Create: resourceVPCEndpointServiceCreate,
		Read:   resourceVPCEndpointServiceRead,
		Update: resourceVPCEndpointServiceUpdate,
		Delete: resourceVPCEndpointServiceDelete,
		Importer: &schema.ResourceImporter{
			State: schema.ImportStatePassthrough,
		},

		Schema: map[string]*schema.Schema{
			"acceptance_required": {
				Type:     schema.TypeBool,
				Required: true,
			},
			"allowed_principals": {
				Type:     schema.TypeSet,
				Optional: true,
				Computed: true,
				Elem:     &schema.Schema{Type: schema.TypeString},
				Set:      schema.HashString,
			},
			"arn": {
				Type:     schema.TypeString,
				Computed: true,
			},
			"availability_zones": {
				Type:     schema.TypeSet,
				Elem:     &schema.Schema{Type: schema.TypeString},
				Computed: true,
				Set:      schema.HashString,
			},
			"base_endpoint_dns_names": {
				Type:     schema.TypeSet,
				Elem:     &schema.Schema{Type: schema.TypeString},
				Computed: true,
				Set:      schema.HashString,
			},
			"gateway_load_balancer_arns": {
				Type:     schema.TypeSet,
				Optional: true,
				MinItems: 1,
				Elem: &schema.Schema{
					Type:         schema.TypeString,
					ValidateFunc: verify.ValidARN,
				},
				Set: schema.HashString,
			},
			"manages_vpc_endpoints": {
				Type:     schema.TypeBool,
				Computed: true,
			},
			"network_load_balancer_arns": {
				Type:     schema.TypeSet,
				Optional: true,
				MinItems: 1,
				Elem: &schema.Schema{
					Type:         schema.TypeString,
					ValidateFunc: verify.ValidARN,
				},
				Set: schema.HashString,
			},
			"private_dns_name": {
				Type:     schema.TypeString,
				Computed: true,
				Optional: true,
			},
			"private_dns_name_configuration": {
				Type:     schema.TypeList,
				Computed: true,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"name": {
							Type:     schema.TypeString,
							Computed: true,
						},
						"state": {
							Type:     schema.TypeString,
							Computed: true,
						},
						"type": {
							Type:     schema.TypeString,
							Computed: true,
						},
						"value": {
							Type:     schema.TypeString,
							Computed: true,
						},
					},
				},
			},
			"service_name": {
				Type:     schema.TypeString,
				Computed: true,
			},
			"service_type": {
				Type:     schema.TypeString,
				Computed: true,
			},
			"state": {
				Type:     schema.TypeString,
				Computed: true,
			},
			"tags":     tftags.TagsSchema(),
			"tags_all": tftags.TagsSchemaComputed(),
		},

		CustomizeDiff: verify.SetTagsDiff,
	}
}

func resourceVPCEndpointServiceCreate(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*conns.AWSClient).EC2Conn
	defaultTagsConfig := meta.(*conns.AWSClient).DefaultTagsConfig
	tags := defaultTagsConfig.MergeTags(tftags.New(d.Get("tags").(map[string]interface{})))

	req := &ec2.CreateVpcEndpointServiceConfigurationInput{
		AcceptanceRequired: aws.Bool(d.Get("acceptance_required").(bool)),
		TagSpecifications:  tagSpecificationsFromKeyValueTags(tags, "vpc-endpoint-service"),
	}
	if v, ok := d.GetOk("private_dns_name"); ok {
		req.PrivateDnsName = aws.String(v.(string))
	}

	if v, ok := d.GetOk("gateway_load_balancer_arns"); ok {
		if v, ok := v.(*schema.Set); ok && v.Len() > 0 {
			req.GatewayLoadBalancerArns = flex.ExpandStringSet(v)
		}
	}

	if v, ok := d.GetOk("network_load_balancer_arns"); ok {
		if v, ok := v.(*schema.Set); ok && v.Len() > 0 {
			req.NetworkLoadBalancerArns = flex.ExpandStringSet(v)
		}
	}

	log.Printf("[DEBUG] Creating VPC Endpoint Service configuration: %#v", req)
	resp, err := conn.CreateVpcEndpointServiceConfiguration(req)
	if err != nil {
		return fmt.Errorf("Error creating VPC Endpoint Service configuration: %s", err.Error())
	}

	d.SetId(aws.StringValue(resp.ServiceConfiguration.ServiceId))

	if err := vpcEndpointServiceWaitUntilAvailable(d, conn); err != nil {
		return err
	}

	if v, ok := d.GetOk("allowed_principals"); ok && v.(*schema.Set).Len() > 0 {
		modifyPermReq := &ec2.ModifyVpcEndpointServicePermissionsInput{
			ServiceId:            aws.String(d.Id()),
			AddAllowedPrincipals: flex.ExpandStringSet(v.(*schema.Set)),
		}
		log.Printf("[DEBUG] Adding VPC Endpoint Service permissions: %#v", modifyPermReq)
		if _, err := conn.ModifyVpcEndpointServicePermissions(modifyPermReq); err != nil {
			return fmt.Errorf("error adding VPC Endpoint Service permissions: %s", err.Error())
		}
	}

	return resourceVPCEndpointServiceRead(d, meta)
}

func resourceVPCEndpointServiceRead(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*conns.AWSClient).EC2Conn
	defaultTagsConfig := meta.(*conns.AWSClient).DefaultTagsConfig
	ignoreTagsConfig := meta.(*conns.AWSClient).IgnoreTagsConfig

	svcCfgRaw, state, err := vpcEndpointServiceStateRefresh(conn, d.Id())()
	if err != nil && state != ec2.ServiceStateFailed {
		return fmt.Errorf("error reading VPC Endpoint Service (%s): %s", d.Id(), err.Error())
	}

	terminalStates := map[string]bool{
		ec2.ServiceStateDeleted:  true,
		ec2.ServiceStateDeleting: true,
		ec2.ServiceStateFailed:   true,
	}
	if _, ok := terminalStates[state]; ok {
		log.Printf("[WARN] VPC Endpoint Service (%s) not found, removing from state", d.Id())
		d.SetId("")
		return nil
	}

	arn := arn.ARN{
		Partition: meta.(*conns.AWSClient).Partition,
		Service:   ec2.ServiceName,
		Region:    meta.(*conns.AWSClient).Region,
		AccountID: meta.(*conns.AWSClient).AccountID,
		Resource:  fmt.Sprintf("vpc-endpoint-service/%s", d.Id()),
	}.String()
	d.Set("arn", arn)

	svcCfg := svcCfgRaw.(*ec2.ServiceConfiguration)
	d.Set("acceptance_required", svcCfg.AcceptanceRequired)
	err = d.Set("availability_zones", flex.FlattenStringSet(svcCfg.AvailabilityZones))
	if err != nil {
		return fmt.Errorf("error setting availability_zones: %s", err)
	}
	err = d.Set("base_endpoint_dns_names", flex.FlattenStringSet(svcCfg.BaseEndpointDnsNames))
	if err != nil {
		return fmt.Errorf("error setting base_endpoint_dns_names: %s", err)
	}

	if err := d.Set("gateway_load_balancer_arns", flex.FlattenStringSet(svcCfg.GatewayLoadBalancerArns)); err != nil {
		return fmt.Errorf("error setting gateway_load_balancer_arns: %w", err)
	}

	d.Set("manages_vpc_endpoints", svcCfg.ManagesVpcEndpoints)

	if err := d.Set("network_load_balancer_arns", flex.FlattenStringSet(svcCfg.NetworkLoadBalancerArns)); err != nil {
		return fmt.Errorf("error setting network_load_balancer_arns: %w", err)
	}

	d.Set("private_dns_name", svcCfg.PrivateDnsName)
	d.Set("service_name", svcCfg.ServiceName)
	d.Set("service_type", svcCfg.ServiceType[0].ServiceType)
	d.Set("state", svcCfg.ServiceState)

	tags := KeyValueTags(svcCfg.Tags).IgnoreAWS().IgnoreConfig(ignoreTagsConfig)

	//lintignore:AWSR002
	if err := d.Set("tags", tags.RemoveDefaultConfig(defaultTagsConfig).Map()); err != nil {
		return fmt.Errorf("error setting tags: %w", err)
	}

	if err := d.Set("tags_all", tags.Map()); err != nil {
		return fmt.Errorf("error setting tags_all: %w", err)
	}

	resp, err := conn.DescribeVpcEndpointServicePermissions(&ec2.DescribeVpcEndpointServicePermissionsInput{
		ServiceId: aws.String(d.Id()),
	})
	if err != nil {
		return fmt.Errorf("error reading VPC Endpoint Service permissions (%s): %s", d.Id(), err.Error())
	}

	err = d.Set("allowed_principals", flattenVPCEndpointServiceAllowedPrincipals(resp.AllowedPrincipals))
	if err != nil {
		return fmt.Errorf("error setting allowed_principals: %s", err)
	}

	err = d.Set("private_dns_name_configuration", flattenPrivateDNSNameConfiguration(svcCfg.PrivateDnsNameConfiguration))
	if err != nil {
		return fmt.Errorf("error setting private_dns_name_configuration: %w", err)
	}

	return nil
}

func flattenPrivateDNSNameConfiguration(privateDnsNameConfiguration *ec2.PrivateDnsNameConfiguration) []interface{} {
	if privateDnsNameConfiguration == nil {
		return nil
	}
	tfMap := map[string]interface{}{}

	if v := privateDnsNameConfiguration.Name; v != nil {
		tfMap["name"] = aws.StringValue(v)
	}

	if v := privateDnsNameConfiguration.State; v != nil {
		tfMap["state"] = aws.StringValue(v)
	}

	if v := privateDnsNameConfiguration.Type; v != nil {
		tfMap["type"] = aws.StringValue(v)
	}

	if v := privateDnsNameConfiguration.Value; v != nil {
		tfMap["value"] = aws.StringValue(v)
	}

	// The EC2 API can return a XML structure with no elements
	if len(tfMap) == 0 {
		return nil
	}

	return []interface{}{tfMap}
}

func resourceVPCEndpointServiceUpdate(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*conns.AWSClient).EC2Conn

	if d.HasChanges("acceptance_required", "gateway_load_balancer_arns", "network_load_balancer_arns", "private_dns_name") {
		modifyCfgReq := &ec2.ModifyVpcEndpointServiceConfigurationInput{
			ServiceId: aws.String(d.Id()),
		}

		if d.HasChange("private_dns_name") {
			modifyCfgReq.PrivateDnsName = aws.String(d.Get("private_dns_name").(string))
		}

		if d.HasChange("acceptance_required") {
			modifyCfgReq.AcceptanceRequired = aws.Bool(d.Get("acceptance_required").(bool))
		}

		setVPCEndpointServiceUpdateLists(d, "gateway_load_balancer_arns",
			&modifyCfgReq.AddGatewayLoadBalancerArns, &modifyCfgReq.RemoveGatewayLoadBalancerArns)

		setVPCEndpointServiceUpdateLists(d, "network_load_balancer_arns",
			&modifyCfgReq.AddNetworkLoadBalancerArns, &modifyCfgReq.RemoveNetworkLoadBalancerArns)

		log.Printf("[DEBUG] Modifying VPC Endpoint Service configuration: %#v", modifyCfgReq)
		if _, err := conn.ModifyVpcEndpointServiceConfiguration(modifyCfgReq); err != nil {
			return fmt.Errorf("Error modifying VPC Endpoint Service configuration: %s", err.Error())
		}

		if err := vpcEndpointServiceWaitUntilAvailable(d, conn); err != nil {
			return err
		}
	}

	if d.HasChange("allowed_principals") {
		modifyPermReq := &ec2.ModifyVpcEndpointServicePermissionsInput{
			ServiceId: aws.String(d.Id()),
		}

		setVPCEndpointServiceUpdateLists(d, "allowed_principals",
			&modifyPermReq.AddAllowedPrincipals, &modifyPermReq.RemoveAllowedPrincipals)

		log.Printf("[DEBUG] Modifying VPC Endpoint Service permissions: %#v", modifyPermReq)
		if _, err := conn.ModifyVpcEndpointServicePermissions(modifyPermReq); err != nil {
			return fmt.Errorf("Error modifying VPC Endpoint Service permissions: %s", err.Error())
		}
	}

	if d.HasChange("tags_all") {
		o, n := d.GetChange("tags_all")

		if err := UpdateTags(conn, d.Id(), o, n); err != nil {
			return fmt.Errorf("error updating EC2 VPC Endpoint Service (%s) tags: %s", d.Id(), err)
		}
	}

	return resourceVPCEndpointServiceRead(d, meta)
}

func resourceVPCEndpointServiceDelete(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*conns.AWSClient).EC2Conn

	input := &ec2.DeleteVpcEndpointServiceConfigurationsInput{
		ServiceIds: aws.StringSlice([]string{d.Id()}),
	}

	output, err := conn.DeleteVpcEndpointServiceConfigurations(input)

	if tfawserr.ErrCodeEquals(err, errCodeInvalidVPCEndpointServiceIDNotFound) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("error deleting EC2 VPC Endpoint Service (%s): %w", d.Id(), err)
	}

	if output != nil && len(output.Unsuccessful) > 0 {
		err := UnsuccessfulItemsError(output.Unsuccessful)

		if err != nil {
			return fmt.Errorf("error deleting EC2 VPC Endpoint Service (%s): %w", d.Id(), err)
		}
	}

	if err := waitForVPCEndpointServiceDeletion(conn, d.Id()); err != nil {
		return fmt.Errorf("error waiting for EC2 VPC Endpoint Service (%s) to delete: %w", d.Id(), err)
	}

	return nil
}

func vpcEndpointServiceStateRefresh(conn *ec2.EC2, svcId string) resource.StateRefreshFunc {
	return func() (interface{}, string, error) {
		log.Printf("[DEBUG] Reading VPC Endpoint Service Configuration: %s", svcId)
		resp, err := conn.DescribeVpcEndpointServiceConfigurations(&ec2.DescribeVpcEndpointServiceConfigurationsInput{
			ServiceIds: aws.StringSlice([]string{svcId}),
		})
		if err != nil {
			if tfawserr.ErrCodeEquals(err, "InvalidVpcEndpointServiceId.NotFound") {
				return false, ec2.ServiceStateDeleted, nil
			}

			return nil, "", err
		}

		svcCfg := resp.ServiceConfigurations[0]
		state := aws.StringValue(svcCfg.ServiceState)
		// No use in retrying if the endpoint service is in a failed state.
		if state == ec2.ServiceStateFailed {
			return nil, state, errors.New("VPC Endpoint Service is in a failed state")
		}
		return svcCfg, state, nil
	}
}

func vpcEndpointServiceWaitUntilAvailable(d *schema.ResourceData, conn *ec2.EC2) error {
	stateConf := &resource.StateChangeConf{
		Pending:    []string{ec2.ServiceStatePending},
		Target:     []string{ec2.ServiceStateAvailable},
		Refresh:    vpcEndpointServiceStateRefresh(conn, d.Id()),
		Timeout:    10 * time.Minute,
		Delay:      5 * time.Second,
		MinTimeout: 5 * time.Second,
	}
	if _, err := stateConf.WaitForState(); err != nil {
		return fmt.Errorf("Error waiting for VPC Endpoint Service %s to become available: %s", d.Id(), err.Error())
	}

	return nil
}

func waitForVPCEndpointServiceDeletion(conn *ec2.EC2, serviceID string) error {
	stateConf := &resource.StateChangeConf{
		Pending:    []string{ec2.ServiceStateAvailable, ec2.ServiceStateDeleting},
		Target:     []string{ec2.ServiceStateDeleted},
		Refresh:    vpcEndpointServiceStateRefresh(conn, serviceID),
		Timeout:    10 * time.Minute,
		Delay:      5 * time.Second,
		MinTimeout: 5 * time.Second,
	}

	_, err := stateConf.WaitForState()

	return err
}

func setVPCEndpointServiceUpdateLists(d *schema.ResourceData, key string, a, r *[]*string) {
	if d.HasChange(key) {
		o, n := d.GetChange(key)
		os := o.(*schema.Set)
		ns := n.(*schema.Set)

		add := flex.ExpandStringSet(ns.Difference(os))
		if len(add) > 0 {
			*a = add
		}

		remove := flex.ExpandStringSet(os.Difference(ns))
		if len(remove) > 0 {
			*r = remove
		}
	}
}

func flattenVPCEndpointServiceAllowedPrincipals(allowedPrincipals []*ec2.AllowedPrincipal) *schema.Set {
	vPrincipals := []interface{}{}

	for _, allowedPrincipal := range allowedPrincipals {
		if allowedPrincipal.Principal != nil {
			vPrincipals = append(vPrincipals, aws.StringValue(allowedPrincipal.Principal))
		}
	}

	return schema.NewSet(schema.HashString, vPrincipals)
}
