# frozen_string_literal: true

Vagrant.configure("2") do |config|
  config.vm.box = ENV.fetch("DEVRAIL_VAGRANT_BOX", "bento/ubuntu-24.04")
  config.vm.hostname = "devrail-router-smoke"

  config.vm.provider "virtualbox" do |vb|
    vb.name = "devrail-router-smoke"
    vb.cpus = 2
    vb.memory = 2048
  end

  config.vm.provider "libvirt" do |lv|
    lv.cpus = 2
    lv.memory = 2048
  end

  config.vm.provision "shell", path: "test/smoke/vagrant-provision.sh"
end
