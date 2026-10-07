# A Kubernetes cluster is imported by its id. node_plan cannot be read back:
# set it in the configuration, and the next apply records it without replacing
# the cluster. The kubeconfig is downloaded on the first refresh.
terraform import pantechdynamics_kubernetes_cluster.prod k8s_06ggex1abcdefghjkmnpqrstvw
