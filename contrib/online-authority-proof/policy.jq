(.services | keys) == ["authority-env", "client-env"] and
(.volumes | keys) == ["authority-state", "client-state"] and
(.secrets // {} | keys) == [] and
(.configs // {} | keys) == [] and
(.networks | keys) == ["lab"] and
(.volumes["authority-state"] | keys) == ["labels", "name"] and
(.volumes["client-state"] | keys) == ["labels", "name"] and
(.volumes["authority-state"].name == ($project + "_authority-state")) and
(.volumes["client-state"].name == ($project + "_client-state")) and
(.volumes["authority-state"].labels["com.procrastivity.wip.authority-proof"] == "lab-v1") and
(.volumes["client-state"].labels["com.procrastivity.wip.authority-proof"] == "lab-v1") and
((.networks.lab | keys) == ["internal", "ipam", "labels", "name"]) and
(.networks.lab.name == ($project + "_lab")) and
(.networks.lab.labels["com.procrastivity.wip.authority-proof"] == "lab-v1") and
(.services["authority-env"].networks | keys) == ["lab"] and
(.services["client-env"].networks | keys) == ["lab"] and
(.services["authority-env"].environment.LAB_ENVIRONMENT == "authority") and
(.services["client-env"].environment.LAB_ENVIRONMENT == "client") and
all(.services[];
  .image == "alpine:3.22.1@sha256:4bcff63911fcb4448bd4fdacec207030997caf25e9bea4045fa6c8c44de311d1" and
  (.build // null) == null and
  (.pull_policy // "") == "") and
(.services["authority-env"].volumes | length) == 1 and
(.services["client-env"].volumes | length) == 1 and
(.services["authority-env"].volumes[0].type == "volume") and
(.services["client-env"].volumes[0].type == "volume") and
(.services["authority-env"].volumes[0].source == "authority-state") and
(.services["client-env"].volumes[0].source == "client-state") and
(.services["authority-env"].volumes[0].target == "/var/lib/online-authority-proof") and
(.services["client-env"].volumes[0].target == "/var/lib/online-authority-proof") and
all(.services[];
  (.privileged // false) == false and
  (.network_mode // "") != "host" and
  (.pid // "") != "host" and
  (.ipc // "") != "host" and
  (.pid // "") == "" and
  (.ipc // "") == "" and
  (.read_only // false) == true and
  ((.cap_add // []) | length) == 0 and
  ((.cap_drop // []) | index("ALL")) != null and
  ((.security_opt // []) | index("no-new-privileges:true")) != null and
  ((.devices // []) | length) == 0 and
  ((.ports // []) | length) == 0 and
  ((.secrets // []) | length) == 0 and
  ((.configs // []) | length) == 0 and
  ((.env_file // []) | length) == 0 and
  ((.volumes_from // []) | length) == 0 and
  (.labels["com.procrastivity.wip.authority-proof"] == "lab-v1") and
  all(.volumes[]?; .type == "volume") and
  all((.environment // {}) | to_entries[]?;
    .key == "LAB_ENVIRONMENT")) and
all(.networks[]; (.internal // false) == true)
