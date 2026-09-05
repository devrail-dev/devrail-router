# frozen_string_literal: true

host_cpu = RbConfig::CONFIG.fetch("host_cpu", "")
default_box = host_cpu.include?("arm64") || host_cpu.include?("aarch64") ? "perk/ubuntu-2204-arm64" : "bento/ubuntu-24.04"

Vagrant.configure("2") do |config|
  config.vm.box = ENV.fetch("DEVRAIL_VAGRANT_BOX", default_box)
  config.vm.hostname = "devrail-router-smoke"
  config.vm.synced_folder ".", "/vagrant",
                          type: "rsync",
                          rsync__exclude: [".git/", ".vagrant/"]

  config.vm.provider "qemu" do |qe|
    qe.machine = "virt"
    qe.cpu = host_cpu.include?("arm64") || host_cpu.include?("aarch64") ? "max" : "host"
    qe.smp = "2"
    qe.memory = "2048"
  end

  config.vm.provider "virtualbox" do |vb|
    vb.name = "devrail-router-smoke"
    vb.cpus = 2
    vb.memory = 2048
  end

  config.vm.provider "libvirt" do |lv|
    lv.cpus = 2
    lv.memory = 2048
  end

  config.vm.provision "shell",
                      path: "test/smoke/vagrant-provision.sh",
                      env: {
                        "DEVRAIL_VAGRANT_GOARCH" => ENV.fetch("DEVRAIL_VAGRANT_GOARCH", host_cpu.include?("arm64") || host_cpu.include?("aarch64") ? "arm64" : "amd64")
                      }
end
