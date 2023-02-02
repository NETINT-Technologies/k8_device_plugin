#Version: 2.0.0
FROM centos:centos7
MAINTAINER Tyroun Liu "tyroun.liu@netint.ca"
ENV REFRESHED_AT 2023-01-12
#should build netint first
COPY netint /root/netint
#should config proxy in yum/proxy/yum.conf for docker 
COPY yum/proxy/yum.conf /etc/

#nvme cli install
RUN yum install -y nvme-cli.x86_64

#back to default yum.conf 
COPY yum/default/yum.conf /etc/

CMD ["/root/netint"] 

EXPOSE 80
