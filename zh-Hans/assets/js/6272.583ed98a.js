"use strict";(self.webpackChunkdocs_site=self.webpackChunkdocs_site||[]).push([["6272"],{9283(e,t,i){i.r(t),i.d(t,{default:()=>u});var s=i(4848),r=i(6540),o=i(4922),n=i(9437);let a=["#5227FF","#FF9FFC","#B497CF"];function u({mouseForce:e=20,cursorSize:t=100,isViscous:i=!1,viscous:l=30,iterationsViscous:c=32,iterationsPoisson:h=32,dt:v=.014,BFECC:p=!0,resolution:d=.5,isBounce:m=!1,colors:f=a,style:y={},className:x="",autoDemo:g=!0,autoSpeed:w=.5,autoIntensity:_=2.2,takeoverDuration:b=.25,autoResumeDelay:S=1e3,autoRampDuration:D=.6}){let T=(0,r.useRef)(null),z=(0,r.useRef)(null),C=(0,r.useRef)(null),M=(0,r.useRef)(null),F=(0,r.useRef)(null),I=(0,r.useRef)(!0),k=(0,r.useRef)(null);return(0,r.useEffect)(()=>{if(!T.current)return;let s=function(e){let t,i=(t=Array.isArray(e)&&e.length>0?1===e.length?[e[0],e[0]]:e:["#ffffff","#ffffff"]).length,s=new Uint8Array(4*i);for(let e=0;e<i;e++){let i=new o.Q1f(t[e]);s[4*e+0]=Math.round(255*i.r),s[4*e+1]=Math.round(255*i.g),s[4*e+2]=Math.round(255*i.b),s[4*e+3]=255}let r=new o.GYF(s,i,1,1023);return r.magFilter=1006,r.minFilter=1006,r.wrapS=1001,r.wrapT=1001,r.generateMipmaps=!1,r.needsUpdate=!0,r}(f),r=new o.IUQ(0,0,0,0),a=new class{width=0;height=0;aspect=1;pixelRatio=1;isMobile=!1;breakpoint=768;fboWidth=null;fboHeight=null;time=0;delta=0;container=null;renderer=null;clock=null;init(e){this.container=e,this.pixelRatio=Math.min(window.devicePixelRatio||1,2),this.resize(),this.renderer=new n.JeP({antialias:!0,alpha:!0}),this.renderer.autoClear=!1,this.renderer.setClearColor(new o.Q1f(0),0),this.renderer.setPixelRatio(this.pixelRatio),this.renderer.setSize(this.width,this.height);let t=this.renderer.domElement;t.style.width="100%",t.style.height="100%",t.style.display="block",this.clock=new o.zD7,this.clock.start()}resize(){if(!this.container)return;let e=this.container.getBoundingClientRect();this.width=Math.max(1,Math.floor(e.width)),this.height=Math.max(1,Math.floor(e.height)),this.aspect=this.width/this.height,this.renderer&&this.renderer.setSize(this.width,this.height,!1)}update(){this.clock&&(this.delta=this.clock.getDelta(),this.time+=this.delta)}};class u{mouseMoved=!1;coords=new o.I9Y;coords_old=new o.I9Y;diff=new o.I9Y;timer=null;container=null;docTarget=null;listenerTarget=null;isHoverInside=!1;hasUserControl=!1;isAutoActive=!1;autoIntensity=2;takeoverActive=!1;takeoverStartTime=0;takeoverDuration=.25;takeoverFrom=new o.I9Y;takeoverTo=new o.I9Y;onInteract=null;_onMouseMove=this.onDocumentMouseMove.bind(this);_onTouchStart=this.onDocumentTouchStart.bind(this);_onTouchMove=this.onDocumentTouchMove.bind(this);_onTouchEnd=this.onTouchEnd.bind(this);_onDocumentLeave=this.onDocumentLeave.bind(this);init(e){this.container=e,this.docTarget=e.ownerDocument||null;let t=this.docTarget?.defaultView||("u">typeof window?window:null);t&&(this.listenerTarget=t,this.listenerTarget.addEventListener("mousemove",this._onMouseMove),this.listenerTarget.addEventListener("touchstart",this._onTouchStart,{passive:!0}),this.listenerTarget.addEventListener("touchmove",this._onTouchMove,{passive:!0}),this.listenerTarget.addEventListener("touchend",this._onTouchEnd),this.docTarget?.addEventListener("mouseleave",this._onDocumentLeave))}dispose(){this.listenerTarget&&(this.listenerTarget.removeEventListener("mousemove",this._onMouseMove),this.listenerTarget.removeEventListener("touchstart",this._onTouchStart),this.listenerTarget.removeEventListener("touchmove",this._onTouchMove),this.listenerTarget.removeEventListener("touchend",this._onTouchEnd)),this.docTarget&&this.docTarget.removeEventListener("mouseleave",this._onDocumentLeave),this.listenerTarget=null,this.docTarget=null,this.container=null}isPointInside(e,t){if(!this.container)return!1;let i=this.container.getBoundingClientRect();return 0!==i.width&&0!==i.height&&e>=i.left&&e<=i.right&&t>=i.top&&t<=i.bottom}updateHoverState(e,t){return this.isHoverInside=this.isPointInside(e,t),this.isHoverInside}setCoords(e,t){if(!this.container)return;this.timer&&window.clearTimeout(this.timer);let i=this.container.getBoundingClientRect();if(0===i.width||0===i.height)return;let s=(e-i.left)/i.width,r=(t-i.top)/i.height;this.coords.set(2*s-1,-(2*r-1)),this.mouseMoved=!0,this.timer=window.setTimeout(()=>{this.mouseMoved=!1},100)}setNormalized(e,t){this.coords.set(e,t),this.mouseMoved=!0}onDocumentMouseMove(e){if(this.updateHoverState(e.clientX,e.clientY)){if(this.onInteract&&this.onInteract(),this.isAutoActive&&!this.hasUserControl&&!this.takeoverActive){if(!this.container)return;let t=this.container.getBoundingClientRect(),i=(e.clientX-t.left)/t.width,s=(e.clientY-t.top)/t.height;this.takeoverFrom.copy(this.coords),this.takeoverTo.set(2*i-1,-(2*s-1)),this.takeoverStartTime=performance.now(),this.takeoverActive=!0,this.hasUserControl=!0,this.isAutoActive=!1;return}this.setCoords(e.clientX,e.clientY),this.hasUserControl=!0}}onDocumentTouchStart(e){if(1!==e.touches.length)return;let t=e.touches[0];this.updateHoverState(t.clientX,t.clientY)&&(this.onInteract&&this.onInteract(),this.setCoords(t.clientX,t.clientY),this.hasUserControl=!0)}onDocumentTouchMove(e){if(1!==e.touches.length)return;let t=e.touches[0];this.updateHoverState(t.clientX,t.clientY)&&(this.onInteract&&this.onInteract(),this.setCoords(t.clientX,t.clientY))}onTouchEnd(){this.isHoverInside=!1}onDocumentLeave(){this.isHoverInside=!1}update(){if(this.takeoverActive){let e=(performance.now()-this.takeoverStartTime)/(1e3*this.takeoverDuration);e>=1?(this.takeoverActive=!1,this.coords.copy(this.takeoverTo),this.coords_old.copy(this.coords),this.diff.set(0,0)):this.coords.copy(this.takeoverFrom).lerp(this.takeoverTo,e*e*(3-2*e))}this.diff.subVectors(this.coords,this.coords_old),this.coords_old.copy(this.coords),0===this.coords_old.x&&0===this.coords_old.y&&this.diff.set(0,0),this.isAutoActive&&!this.takeoverActive&&this.diff.multiplyScalar(this.autoIntensity)}}let y=new u;class x{mouse;manager;enabled;speed;resumeDelay;rampDurationMs;active=!1;current=new o.I9Y(0,0);target=new o.I9Y;lastTime=performance.now();activationTime=0;margin=.2;_tmpDir=new o.I9Y;constructor(e,t,i){this.mouse=e,this.manager=t,this.enabled=i.enabled,this.speed=i.speed,this.resumeDelay=i.resumeDelay||3e3,this.rampDurationMs=1e3*(i.rampDuration||0),this.pickNewTarget()}pickNewTarget(){let e=Math.random;this.target.set((2*e()-1)*(1-this.margin),(2*e()-1)*(1-this.margin))}forceStop(){this.active=!1,this.mouse.isAutoActive=!1}update(){if(!this.enabled)return;let e=performance.now();if(e-this.manager.lastUserInteraction<this.resumeDelay||this.mouse.isHoverInside){this.active&&this.forceStop();return}if(this.active||(this.active=!0,this.current.copy(this.mouse.coords),this.lastTime=e,this.activationTime=e),!this.active)return;this.mouse.isAutoActive=!0;let t=(e-this.lastTime)/1e3;this.lastTime=e,t>.2&&(t=.016);let i=this._tmpDir.subVectors(this.target,this.current),s=i.length();if(s<.01)return void this.pickNewTarget();i.normalize();let r=1;if(this.rampDurationMs>0){let t=Math.min(1,(e-this.activationTime)/this.rampDurationMs);r=t*t*(3-2*t)}let o=Math.min(this.speed*t*r,s);this.current.addScaledVector(i,o),this.mouse.setNormalized(this.current.x,this.current.y)}}let A=`
	attribute vec3 position;
	uniform vec2 px;
	uniform vec2 boundarySpace;
	varying vec2 uv;
	precision highp float;
	void main(){
	vec3 pos = position;
	vec2 scale = 1.0 - boundarySpace * 2.0;
	pos.xy = pos.xy * scale;
	uv = vec2(0.5)+(pos.xy)*0.5;
	gl_Position = vec4(pos, 1.0);
}
`,E=`
	attribute vec3 position;
	uniform vec2 px;
	precision highp float;
	varying vec2 uv;
	void main(){
	vec3 pos = position;
	uv = 0.5 + pos.xy * 0.5;
	vec2 n = sign(pos.xy);
	pos.xy = abs(pos.xy) - px * 1.0;
	pos.xy *= n;
	gl_Position = vec4(pos, 1.0);
}
`,R=`
		precision highp float;
		attribute vec3 position;
		attribute vec2 uv;
		uniform vec2 center;
		uniform vec2 scale;
		uniform vec2 px;
		varying vec2 vUv;
		void main(){
		vec2 pos = position.xy * scale * 2.0 * px + center;
		vUv = uv;
		gl_Position = vec4(pos, 0.0, 1.0);
}
`,B=`
		precision highp float;
		uniform sampler2D velocity;
		uniform float dt;
		uniform bool isBFECC;
		uniform vec2 fboSize;
		uniform vec2 px;
		varying vec2 uv;
		void main(){
		vec2 ratio = max(fboSize.x, fboSize.y) / fboSize;
		if(isBFECC == false){
				vec2 vel = texture2D(velocity, uv).xy;
				vec2 uv2 = uv - vel * dt * ratio;
				vec2 newVel = texture2D(velocity, uv2).xy;
				gl_FragColor = vec4(newVel, 0.0, 0.0);
		} else {
				vec2 spot_new = uv;
				vec2 vel_old = texture2D(velocity, uv).xy;
				vec2 spot_old = spot_new - vel_old * dt * ratio;
				vec2 vel_new1 = texture2D(velocity, spot_old).xy;
				vec2 spot_new2 = spot_old + vel_new1 * dt * ratio;
				vec2 error = spot_new2 - spot_new;
				vec2 spot_new3 = spot_new - error / 2.0;
				vec2 vel_2 = texture2D(velocity, spot_new3).xy;
				vec2 spot_old2 = spot_new3 - vel_2 * dt * ratio;
				vec2 newVel2 = texture2D(velocity, spot_old2).xy; 
				gl_FragColor = vec4(newVel2, 0.0, 0.0);
		}
}
`,Y=`
		precision highp float;
		uniform sampler2D velocity;
		uniform sampler2D palette;
		uniform vec4 bgColor;
		varying vec2 uv;
		void main(){
		vec2 vel = texture2D(velocity, uv).xy;
		float lenv = clamp(length(vel), 0.0, 1.0);
		vec3 c = texture2D(palette, vec2(lenv, 0.5)).rgb;
		vec3 outRGB = mix(bgColor.rgb, c, lenv);
		float outA = mix(bgColor.a, 1.0, lenv);
		gl_FragColor = vec4(outRGB, outA);
}
`,L=`
		precision highp float;
		uniform sampler2D velocity;
		uniform float dt;
		uniform vec2 px;
		varying vec2 uv;
		void main(){
		float x0 = texture2D(velocity, uv-vec2(px.x, 0.0)).x;
		float x1 = texture2D(velocity, uv+vec2(px.x, 0.0)).x;
		float y0 = texture2D(velocity, uv-vec2(0.0, px.y)).y;
		float y1 = texture2D(velocity, uv+vec2(0.0, px.y)).y;
		float divergence = (x1 - x0 + y1 - y0) / 2.0;
		gl_FragColor = vec4(divergence / dt);
}
`,P=`
		precision highp float;
		uniform vec2 force;
		uniform vec2 center;
		uniform vec2 scale;
		uniform vec2 px;
		varying vec2 vUv;
		void main(){
		vec2 circle = (vUv - 0.5) * 2.0;
		float d = 1.0 - min(length(circle), 1.0);
		d *= d;
		gl_FragColor = vec4(force * d, 0.0, 1.0);
}
`,U=`
		precision highp float;
		uniform sampler2D pressure;
		uniform sampler2D divergence;
		uniform vec2 px;
		varying vec2 uv;
		void main(){
		float p0 = texture2D(pressure, uv + vec2(px.x * 2.0, 0.0)).r;
		float p1 = texture2D(pressure, uv - vec2(px.x * 2.0, 0.0)).r;
		float p2 = texture2D(pressure, uv + vec2(0.0, px.y * 2.0)).r;
		float p3 = texture2D(pressure, uv - vec2(0.0, px.y * 2.0)).r;
		float div = texture2D(divergence, uv).r;
		float newP = (p0 + p1 + p2 + p3) / 4.0 - div;
		gl_FragColor = vec4(newP);
}
`,V=`
		precision highp float;
		uniform sampler2D pressure;
		uniform sampler2D velocity;
		uniform vec2 px;
		uniform float dt;
		varying vec2 uv;
		void main(){
		float step = 1.0;
		float p0 = texture2D(pressure, uv + vec2(px.x * step, 0.0)).r;
		float p1 = texture2D(pressure, uv - vec2(px.x * step, 0.0)).r;
		float p2 = texture2D(pressure, uv + vec2(0.0, px.y * step)).r;
		float p3 = texture2D(pressure, uv - vec2(0.0, px.y * step)).r;
		vec2 v = texture2D(velocity, uv).xy;
		vec2 gradP = vec2(p0 - p1, p2 - p3) * 0.5;
		v = v - gradP * dt;
		gl_FragColor = vec4(v, 0.0, 1.0);
}
`,H=`
		precision highp float;
		uniform sampler2D velocity;
		uniform sampler2D velocity_new;
		uniform float v;
		uniform vec2 px;
		uniform float dt;
		varying vec2 uv;
		void main(){
		vec2 old = texture2D(velocity, uv).xy;
		vec2 new0 = texture2D(velocity_new, uv + vec2(px.x * 2.0, 0.0)).xy;
		vec2 new1 = texture2D(velocity_new, uv - vec2(px.x * 2.0, 0.0)).xy;
		vec2 new2 = texture2D(velocity_new, uv + vec2(0.0, px.y * 2.0)).xy;
		vec2 new3 = texture2D(velocity_new, uv - vec2(0.0, px.y * 2.0)).xy;
		vec2 newv = 4.0 * old + v * dt * (new0 + new1 + new2 + new3);
		newv /= 4.0 * (1.0 + v * dt);
		gl_FragColor = vec4(newv, 0.0, 0.0);
}
`;class ${props;uniforms;scene=null;camera=null;material=null;geometry=null;plane=null;constructor(e){this.props=e||{},this.uniforms=this.props.material?.uniforms}init(){this.scene=new o.Z58,this.camera=new o.i7d,this.uniforms&&(this.material=new o.D$Q(this.props.material),this.geometry=new o.bdM(2,2),this.plane=new o.eaF(this.geometry,this.material),this.scene.add(this.plane))}update(){a.renderer&&this.scene&&this.camera&&(a.renderer.setRenderTarget(this.props.output||null),a.renderer.render(this.scene,this.camera),a.renderer.setRenderTarget(null))}}class N extends ${line;constructor(e){super({material:{vertexShader:A,fragmentShader:B,uniforms:{boundarySpace:{value:e.cellScale},px:{value:e.cellScale},fboSize:{value:e.fboSize},velocity:{value:e.src.texture},dt:{value:e.dt},isBFECC:{value:!0}}},output:e.dst}),this.uniforms=this.props.material.uniforms,this.init()}init(){super.init(),this.createBoundary()}createBoundary(){let e=new o.LoY,t=new Float32Array([-1,-1,0,-1,1,0,-1,1,0,1,1,0,1,1,0,1,-1,0,1,-1,0,-1,-1,0]);e.setAttribute("position",new o.THS(t,3));let i=new o.D$Q({vertexShader:E,fragmentShader:B,uniforms:this.uniforms});this.line=new o.DXC(e,i),this.scene.add(this.line)}update(...e){let{dt:t,isBounce:i,BFECC:s}=e[0]||{};this.uniforms&&("number"==typeof t&&(this.uniforms.dt.value=t),"boolean"==typeof i&&(this.line.visible=i),"boolean"==typeof s&&(this.uniforms.isBFECC.value=s),super.update())}}class X extends ${mouse;constructor(e){super({output:e.dst}),this.init(e)}init(e){super.init();let t=new o.bdM(1,1),i=new o.D$Q({vertexShader:R,fragmentShader:P,blending:2,depthWrite:!1,uniforms:{px:{value:e.cellScale},force:{value:new o.I9Y(0,0)},center:{value:new o.I9Y(0,0)},scale:{value:new o.I9Y(e.cursor_size,e.cursor_size)}}});this.mouse=new o.eaF(t,i),this.scene.add(this.mouse)}update(...e){let t=e[0]||{},i=y.diff.x/2*(t.mouse_force||0),s=y.diff.y/2*(t.mouse_force||0),r=t.cellScale||{x:1,y:1},o=t.cursor_size||0,n=o*r.x,a=o*r.y,u=Math.min(Math.max(y.coords.x,-1+n+2*r.x),1-n-2*r.x),l=Math.min(Math.max(y.coords.y,-1+a+2*r.y),1-a-2*r.y),c=this.mouse.material.uniforms;c.force.value.set(i,s),c.center.value.set(u,l),c.scale.value.set(o,o),super.update()}}class Q extends ${constructor(e){super({material:{vertexShader:A,fragmentShader:H,uniforms:{boundarySpace:{value:e.boundarySpace},velocity:{value:e.src.texture},velocity_new:{value:e.dst_.texture},v:{value:e.viscous},px:{value:e.cellScale},dt:{value:e.dt}}},output:e.dst,output0:e.dst_,output1:e.dst}),this.init()}update(...e){let t,i,{viscous:s,iterations:r,dt:o}=e[0]||{};if(!this.uniforms)return;"number"==typeof s&&(this.uniforms.v.value=s);let n=r??0;for(let e=0;e<n;e++)e%2==0?(t=this.props.output0,i=this.props.output1):(t=this.props.output1,i=this.props.output0),this.uniforms.velocity_new.value=t.texture,this.props.output=i,"number"==typeof o&&(this.uniforms.dt.value=o),super.update();return i}}class O extends ${constructor(e){super({material:{vertexShader:A,fragmentShader:L,uniforms:{boundarySpace:{value:e.boundarySpace},velocity:{value:e.src.texture},px:{value:e.cellScale},dt:{value:e.dt}}},output:e.dst}),this.init()}update(...e){let{vel:t}=e[0]||{};this.uniforms&&t&&(this.uniforms.velocity.value=t.texture),super.update()}}class W extends ${constructor(e){super({material:{vertexShader:A,fragmentShader:U,uniforms:{boundarySpace:{value:e.boundarySpace},pressure:{value:e.dst_.texture},divergence:{value:e.src.texture},px:{value:e.cellScale}}},output:e.dst,output0:e.dst_,output1:e.dst}),this.init()}update(...e){let t,i,{iterations:s}=e[0]||{},r=s??0;for(let e=0;e<r;e++)e%2==0?(t=this.props.output0,i=this.props.output1):(t=this.props.output1,i=this.props.output0),this.uniforms&&(this.uniforms.pressure.value=t.texture),this.props.output=i,super.update();return i}}class j extends ${constructor(e){super({material:{vertexShader:A,fragmentShader:V,uniforms:{boundarySpace:{value:e.boundarySpace},pressure:{value:e.src_p.texture},velocity:{value:e.src_v.texture},px:{value:e.cellScale},dt:{value:e.dt}}},output:e.dst}),this.init()}update(...e){let{vel:t,pressure:i}=e[0]||{};this.uniforms&&t&&i&&(this.uniforms.velocity.value=t.texture,this.uniforms.pressure.value=i.texture),super.update()}}class q{options;fbos={vel_0:null,vel_1:null,vel_viscous0:null,vel_viscous1:null,div:null,pressure_0:null,pressure_1:null};fboSize=new o.I9Y;cellScale=new o.I9Y;boundarySpace=new o.I9Y;advection;externalForce;viscous;divergence;poisson;pressure;constructor(e){this.options={iterations_poisson:32,iterations_viscous:32,mouse_force:20,resolution:.5,cursor_size:100,viscous:30,isBounce:!1,dt:.014,isViscous:!1,BFECC:!0,...e},this.init()}init(){this.calcSize(),this.createAllFBO(),this.createShaderPass()}getFloatType(){return/(iPad|iPhone|iPod)/i.test(navigator.userAgent)?1016:1015}createAllFBO(){let e={type:this.getFloatType(),depthBuffer:!1,stencilBuffer:!1,minFilter:1006,magFilter:1006,wrapS:1001,wrapT:1001};for(let t in this.fbos)this.fbos[t]=new o.nWS(this.fboSize.x,this.fboSize.y,e)}createShaderPass(){this.advection=new N({cellScale:this.cellScale,fboSize:this.fboSize,dt:this.options.dt,src:this.fbos.vel_0,dst:this.fbos.vel_1}),this.externalForce=new X({cellScale:this.cellScale,cursor_size:this.options.cursor_size,dst:this.fbos.vel_1}),this.viscous=new Q({cellScale:this.cellScale,boundarySpace:this.boundarySpace,viscous:this.options.viscous,src:this.fbos.vel_1,dst:this.fbos.vel_viscous1,dst_:this.fbos.vel_viscous0,dt:this.options.dt}),this.divergence=new O({cellScale:this.cellScale,boundarySpace:this.boundarySpace,src:this.fbos.vel_viscous0,dst:this.fbos.div,dt:this.options.dt}),this.poisson=new W({cellScale:this.cellScale,boundarySpace:this.boundarySpace,src:this.fbos.div,dst:this.fbos.pressure_1,dst_:this.fbos.pressure_0}),this.pressure=new j({cellScale:this.cellScale,boundarySpace:this.boundarySpace,src_p:this.fbos.pressure_0,src_v:this.fbos.vel_viscous0,dst:this.fbos.vel_0,dt:this.options.dt})}calcSize(){let e=Math.max(1,Math.round(this.options.resolution*a.width)),t=Math.max(1,Math.round(this.options.resolution*a.height));this.cellScale.set(1/e,1/t),this.fboSize.set(e,t)}resize(){for(let e in this.calcSize(),this.fbos)this.fbos[e].setSize(this.fboSize.x,this.fboSize.y)}update(){this.options.isBounce?this.boundarySpace.set(0,0):this.boundarySpace.copy(this.cellScale),this.advection.update({dt:this.options.dt,isBounce:this.options.isBounce,BFECC:this.options.BFECC}),this.externalForce.update({cursor_size:this.options.cursor_size,mouse_force:this.options.mouse_force,cellScale:this.cellScale});let e=this.fbos.vel_1;this.options.isViscous&&(e=this.viscous.update({viscous:this.options.viscous,iterations:this.options.iterations_viscous,dt:this.options.dt})),this.divergence.update({vel:e});let t=this.poisson.update({iterations:this.options.iterations_poisson});this.pressure.update({vel:e,pressure:t})}}class G{simulation;scene;camera;output;constructor(){this.simulation=new q,this.scene=new o.Z58,this.camera=new o.i7d,this.output=new o.eaF(new o.bdM(2,2),new o.D$Q({vertexShader:A,fragmentShader:Y,transparent:!0,depthWrite:!1,uniforms:{velocity:{value:this.simulation.fbos.vel_0.texture},boundarySpace:{value:new o.I9Y},palette:{value:s},bgColor:{value:r}}})),this.scene.add(this.output)}resize(){this.simulation.resize()}render(){a.renderer&&(a.renderer.setRenderTarget(null),a.renderer.render(this.scene,this.camera))}update(){this.simulation.update(),this.render()}}class Z{props;output;autoDriver;lastUserInteraction=performance.now();running=!1;_loop=this.loop.bind(this);_resize=this.resize.bind(this);_onVisibility;constructor(e){this.props=e,a.init(e.$wrapper),y.init(e.$wrapper),y.autoIntensity=e.autoIntensity,y.takeoverDuration=e.takeoverDuration,y.onInteract=()=>{this.lastUserInteraction=performance.now(),this.autoDriver&&this.autoDriver.forceStop()},this.autoDriver=new x(y,this,{enabled:e.autoDemo,speed:e.autoSpeed,resumeDelay:e.autoResumeDelay,rampDuration:e.autoRampDuration}),this.init(),window.addEventListener("resize",this._resize),this._onVisibility=()=>{document.hidden?this.pause():I.current&&this.start()},document.addEventListener("visibilitychange",this._onVisibility)}init(){a.renderer&&(this.props.$wrapper.prepend(a.renderer.domElement),this.output=new G)}resize(){a.resize(),this.output.resize()}render(){this.autoDriver&&this.autoDriver.update(),y.update(),a.update(),this.output.update()}loop(){this.running&&(this.render(),M.current=requestAnimationFrame(this._loop))}start(){this.running||(this.running=!0,this._loop())}pause(){this.running=!1,M.current&&(cancelAnimationFrame(M.current),M.current=null)}dispose(){try{if(window.removeEventListener("resize",this._resize),this._onVisibility&&document.removeEventListener("visibilitychange",this._onVisibility),y.dispose(),a.renderer){let e=a.renderer.domElement;e&&e.parentNode&&e.parentNode.removeChild(e),a.renderer.dispose(),a.renderer.forceContextLoss()}}catch{}}}let J=T.current;J.style.position=J.style.position||"relative",J.style.overflow=J.style.overflow||"hidden";let K=new Z({$wrapper:J,autoDemo:g,autoSpeed:w,autoIntensity:_,takeoverDuration:b,autoResumeDelay:S,autoRampDuration:D});z.current=K,(()=>{if(!z.current)return;let s=z.current.output?.simulation;if(!s)return;let r=s.options.resolution;Object.assign(s.options,{mouse_force:e,cursor_size:t,isViscous:i,viscous:l,iterations_viscous:c,iterations_poisson:h,dt:v,BFECC:p,resolution:d,isBounce:m}),d!==r&&s.resize()})(),K.start();let ee=new IntersectionObserver(e=>{let t=e[0],i=t.isIntersecting&&t.intersectionRatio>0;I.current=i,z.current&&(i&&!document.hidden?z.current.start():z.current.pause())},{threshold:[0,.01,.1]});ee.observe(J),F.current=ee;let et=new ResizeObserver(()=>{z.current&&(k.current&&cancelAnimationFrame(k.current),k.current=requestAnimationFrame(()=>{z.current&&z.current.resize()}))});return et.observe(J),C.current=et,()=>{if(M.current&&cancelAnimationFrame(M.current),C.current)try{C.current.disconnect()}catch{}if(F.current)try{F.current.disconnect()}catch{}z.current&&z.current.dispose(),z.current=null}},[p,t,v,m,i,h,c,e,d,l,f,g,w,_,b,S,D]),(0,r.useEffect)(()=>{let s=z.current;if(!s)return;let r=s.output?.simulation;if(!r)return;let o=r.options.resolution;Object.assign(r.options,{mouse_force:e,cursor_size:t,isViscous:i,viscous:l,iterations_viscous:c,iterations_poisson:h,dt:v,BFECC:p,resolution:d,isBounce:m}),s.autoDriver&&(s.autoDriver.enabled=g,s.autoDriver.speed=w,s.autoDriver.resumeDelay=S,s.autoDriver.rampDurationMs=1e3*D,s.autoDriver.mouse&&(s.autoDriver.mouse.autoIntensity=_,s.autoDriver.mouse.takeoverDuration=b)),d!==o&&r.resize()},[e,t,i,l,c,h,v,p,d,m,g,w,_,b,S,D]),(0,s.jsx)("div",{ref:T,className:`liquid-ether-container ${x||""}`,style:y})}}}]);